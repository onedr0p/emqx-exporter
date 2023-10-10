// Copyright 2016 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"emqx-exporter/client"
	"emqx-exporter/config"
	"emqx-exporter/prober"

	"fmt"
	stdlog "log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/user"
	"runtime"
	"sort"

	"github.com/alecthomas/kingpin/v2"
	promcollectors "github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/common/promlog"
	"github.com/prometheus/common/promlog/flag"
	"gopkg.in/yaml.v2"

	"emqx-exporter/collector"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/version"
	"github.com/prometheus/exporter-toolkit/web"
	"github.com/prometheus/exporter-toolkit/web/kingpinflag"
)

// handler wraps an unfiltered http.Handler but uses a filtered handler,
// created on the fly, if filtering is requested. Create instances with
// newHandler.
type handler struct {
	// exporterMetricsRegistry is a separate registry for the metrics about
	// the exporter itself.
	exporterMetricsRegistry *prometheus.Registry
	includeExporterMetrics  bool
	maxRequests             int
	logger                  log.Logger
}

func (h *handler) innerHandler() (http.Handler, error) {
	emqxCluster := client.NewCluster(h.logger)
	nc, err := collector.NewEMQXCollector(emqxCluster, h.logger)
	if err != nil {
		return nil, fmt.Errorf("couldn't create collector: %s", err)
	}

	level.Info(h.logger).Log("msg", "Enabled collectors")
	collectors := make([]string, 0, len(nc.Collectors))
	for n := range nc.Collectors {
		collectors = append(collectors, n)
	}
	sort.Strings(collectors)
	for _, c := range collectors {
		level.Info(h.logger).Log("collector", c)
	}

	r := prometheus.NewRegistry()
	r.MustRegister(version.NewCollector("emqx_exporter"))
	if err := r.Register(nc); err != nil {
		return nil, fmt.Errorf("couldn't register node collector: %s", err)
	}
	handler := promhttp.HandlerFor(
		prometheus.Gatherers{h.exporterMetricsRegistry, r},
		promhttp.HandlerOpts{
			ErrorLog:            stdlog.New(log.NewStdlibAdapter(level.Error(h.logger)), "", 0),
			ErrorHandling:       promhttp.ContinueOnError,
			MaxRequestsInFlight: h.maxRequests,
			Registry:            h.exporterMetricsRegistry,
		},
	)
	if h.includeExporterMetrics {
		h.exporterMetricsRegistry.MustRegister(
			promcollectors.NewProcessCollector(promcollectors.ProcessCollectorOpts{}),
			promcollectors.NewGoCollector(),
		)
		// Note that we have to use h.exporterMetricsRegistry here to
		// use the same promhttp metrics for all expositions.
		handler = promhttp.InstrumentMetricHandler(
			h.exporterMetricsRegistry, handler,
		)
	}
	return handler, nil
}

func newHandler(includeExporterMetrics bool, maxRequests int, logger log.Logger) http.Handler {
	h := &handler{
		exporterMetricsRegistry: prometheus.NewRegistry(),
		includeExporterMetrics:  includeExporterMetrics,
		maxRequests:             maxRequests,
		logger:                  logger,
	}
	if innerHandler, err := h.innerHandler(); err != nil {
		panic(fmt.Sprintf("Couldn't create metrics handler: %s", err))
	} else {
		return innerHandler
	}
}

var (
	sc = config.NewSafeConfig(prometheus.DefaultRegisterer)

	configFile = kingpin.Flag("config.file", "EMQX exporter configuration file.").Default("/etc/emqx-exporter/config.yaml").String()
)

func init() {
	prometheus.MustRegister(version.NewCollector("emqx_exporter"))
}

func main() {
	var (
		disableExporterMetrics = kingpin.Flag(
			"web.disable-exporter-metrics",
			"Exclude metrics about the exporter itself (promhttp_*, process_*, go_*).",
		).Bool()
		maxRequests = kingpin.Flag(
			"web.max-requests",
			"Maximum number of parallel scrape requests. Use 0 to disable.",
		).Default("40").Int()
		maxProcs = kingpin.Flag(
			"runtime.gomaxprocs", "The target number of CPUs Go will run on (GOMAXPROCS)",
		).Envar("GOMAXPROCS").Default("1").Int()
		toolkitFlags = kingpinflag.AddFlags(kingpin.CommandLine, ":8085")
	)

	promlogConfig := &promlog.Config{}
	flag.AddFlags(kingpin.CommandLine, promlogConfig)
	kingpin.Version(version.Print("emqx-exporter"))
	kingpin.CommandLine.UsageWriter(os.Stdout)
	kingpin.HelpFlag.Short('h')
	kingpin.Parse()

	logger := promlog.New(promlogConfig)
	level.Info(logger).Log("msg", "Starting emqx-exporter", "version", version.Info())
	level.Info(logger).Log("msg", "Build context", "build_context", version.BuildContext())

	if err := sc.ReloadConfig(*configFile); err != nil {
		level.Error(logger).Log("msg", "Error loading config", "err", err)
		os.Exit(1)
	}
	level.Info(logger).Log("msg", "Loaded config file")

	if user, err := user.Current(); err == nil && user.Uid == "0" {
		level.Warn(logger).Log("msg", "EMQX Exporter is running as root user. This exporter is designed to run as unprivileged user, root is not required.")
	}
	runtime.GOMAXPROCS(*maxProcs)
	level.Debug(logger).Log("msg", "Go MAXPROCS", "procs", runtime.GOMAXPROCS(0))

	http.Handle("/metrics", newHandler(!*disableExporterMetrics, *maxRequests, logger))

	http.HandleFunc("/probe", func(w http.ResponseWriter, r *http.Request) {
		sc.Lock()
		probes := sc.C.Probes
		sc.Unlock()
		prober.Handler(w, r, probes, logger, nil)
	})

	http.HandleFunc("/config", func(w http.ResponseWriter, r *http.Request) {
		sc.RLock()
		c, err := yaml.Marshal(sc.C)
		sc.RUnlock()
		if err != nil {
			level.Warn(logger).Log("msg", "Error marshalling configuration", "err", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Write(c)
	})

	landingConfig := web.LandingConfig{
		Name:        "EMQX Exporter",
		Description: "EMQX Exporter",
		Version:     version.Info(),
		Links: []web.LandingLinks{
			{
				Address: "/metrics",
				Text:    "Metrics",
			},
			{
				Address: "/probe",
				Text:    "Probe",
			},
			{
				Address: "/config",
				Text:    "Config",
			},
		},
	}
	landingPage, err := web.NewLandingPage(landingConfig)
	if err != nil {
		level.Error(logger).Log("err", err)
		os.Exit(1)
	}
	http.Handle("/", landingPage)

	server := &http.Server{}
	if err := web.ListenAndServe(server, toolkitFlags, logger); err != nil {
		level.Error(logger).Log("err", err)
		os.Exit(1)
	}
}
