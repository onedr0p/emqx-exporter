package client

import (
	"context"
	"emqx-exporter/collector"
	"emqx-exporter/config"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
)

type cluster struct {
	client   client
	nodeLock sync.RWMutex
}

func NewCluster(metrics *config.Metrics, logger log.Logger) collector.Cluster {
	c := &cluster{}

	go func() {
		httpClient := getHTTPClient(metrics)
		uri := getURI(metrics)
		for {
			client4 := &cluster4x{
				client: httpClient,
				uri:    uri,
			}
			if _, err := client4.getClusterStatus(); err == nil {
				c.client = client4
				level.Info(logger).Log("msg", "cluster4x client created")
				return
			} else {
				level.Debug(logger).Log("msg", "cluster4x client failed", "err", err)
			}

			client5 := &cluster5x{
				client: httpClient,
				uri:    uri,
			}
			if _, err := client5.getClusterStatus(); err == nil {
				c.client = client5
				level.Info(logger).Log("msg", "cluster5x client created")
				return
			} else {
				level.Debug(logger).Log("msg", "cluster5x client failed", "err", err)
			}

			level.Error(logger).Log("msg", "Couldn't create cluster client, will retry it after 5 seconds", "err", "no cluster node found")
			c.client = nil

			select {
			case <-context.Background().Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}()
	return c
}

func (c *cluster) GetLicense() (lic *collector.LicenseInfo, err error) {
	c.nodeLock.Lock()
	defer c.nodeLock.Unlock()
	client := c.getNode()
	if client == nil {
		return
	}

	lic, err = client.getLicense()
	if err != nil || lic == nil {
		return
	}

	lic.RemainingDays = time.Until(time.UnixMilli(lic.Expiration)).Hours() / 24
	lic.RemainingDays, _ = strconv.ParseFloat(fmt.Sprintf("%.1f", lic.RemainingDays), 64)
	return
}

func (c *cluster) GetClusterStatus() (cluster collector.ClusterStatus, err error) {
	c.nodeLock.Lock()
	defer c.nodeLock.Unlock()
	client := c.getNode()
	if client == nil {
		return
	}
	cluster, err = client.getClusterStatus()
	if err != nil {
		err = fmt.Errorf("collect cluster status failed. %w", err)
		return
	}
	return
}

func (c *cluster) GetBrokerMetrics() (brokers *collector.Broker, err error) {
	c.nodeLock.Lock()
	defer c.nodeLock.Unlock()
	client := c.getNode()
	if client == nil {
		return
	}
	brokers, err = client.getBrokerMetrics()
	if err != nil {
		err = fmt.Errorf("collect broker metrics failed. %w", err)
		return
	}
	return
}

func (c *cluster) GetRuleEngineMetrics() (bridges []collector.DataBridge, res []collector.RuleEngine, err error) {
	c.nodeLock.Lock()
	defer c.nodeLock.Unlock()
	client := c.getNode()
	if client == nil {
		return
	}
	bridges, err = client.getDataBridge()
	if err != nil {
		err = fmt.Errorf("collect rule engine data bridge failed. %w", err)
		return
	}
	res, err = client.getRuleEngineMetrics()
	if err != nil {
		err = fmt.Errorf("collect rule engine metrics failed. %w", err)
		return
	}
	return
}

func (c *cluster) GetAuthenticationMetrics() (dataSources []collector.DataSource, auths []collector.Authentication, err error) {
	c.nodeLock.Lock()
	defer c.nodeLock.Unlock()
	client := c.getNode()
	if client == nil {
		return
	}
	dataSources, auths, err = client.getAuthenticationMetrics()
	if err != nil {
		err = fmt.Errorf("collect authentication metrics failed. %w", err)
		return
	}
	return
}

func (c *cluster) GetAuthorizationMetrics() (dataSources []collector.DataSource, auths []collector.Authorization, err error) {
	c.nodeLock.Lock()
	defer c.nodeLock.Unlock()
	client := c.getNode()
	if client == nil {
		return
	}
	dataSources, auths, err = client.getAuthorizationMetrics()
	if err != nil {
		err = fmt.Errorf("collect authorization metrics failed. %w", err)
		return
	}
	return
}

func (c *cluster) getNode() client {
	client := c.client
	return client
}
