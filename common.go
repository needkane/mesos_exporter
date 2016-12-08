package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

type (
	resources struct {
		CPUs  float64 `json:"cpus"`
		Disk  float64 `json:"disk"`
		Mem   float64 `json:"mem"`
		Ports ranges  `json:"ports"`
	}

	task struct {
		Name        string    `json:"name"`
		ID          string    `json:"id"`
		ExecutorID  string    `json:"executor_id"`
		FrameworkID string    `json:"framework_id"`
		SlaveID     string    `json:"slave_id"`
		State       string    `json:"state"`
		Labels      []label   `json:"labels"`
		Resources   resources `json:"resources"`
		Statuses    []status  `json:"statuses"`
	}

	label struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}

	status struct {
		State     string  `json:"state"`
		Timestamp float64 `json:"timestamp"`
	}
)

type metricMap map[string]float64

var (
	notFoundInMap = errors.New("Couldn't find key in map")
)

type settableCounterVec struct {
	desc   *prometheus.Desc
	values []prometheus.Metric
}

func (c *settableCounterVec) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.desc
}

func (c *settableCounterVec) Collect(ch chan<- prometheus.Metric) {
	for _, v := range c.values {
		ch <- v
	}

	c.values = nil
}

func (c *settableCounterVec) Set(value float64, labelValues ...string) {
	c.values = append(c.values, prometheus.MustNewConstMetric(c.desc, prometheus.CounterValue, value, labelValues...))
}

type settableCounter struct {
	desc  *prometheus.Desc
	value prometheus.Metric
}

func (c *settableCounter) Describe(ch chan<- *prometheus.Desc) {
	if c.desc == nil {
		log.Printf("NIL description: %v", c)
	}
	ch <- c.desc
}

func (c *settableCounter) Collect(ch chan<- prometheus.Metric) {
	if c.value == nil {
		log.Printf("NIL value: %v", c)
	}
	ch <- c.value
}

func (c *settableCounter) Set(value float64) {
	c.value = prometheus.MustNewConstMetric(c.desc, prometheus.CounterValue, value)
}

func newSettableCounter(subsystem, name, help string) *settableCounter {
	return &settableCounter{
		desc: prometheus.NewDesc(
			prometheus.BuildFQName("mesos", subsystem, name),
			help,
			nil,
			prometheus.Labels{},
		),
	}
}

func gauge(subsystem, name, help string, labels ...string) *prometheus.GaugeVec {
	return prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "mesos",
		Subsystem: subsystem,
		Name:      name,
		Help:      help,
	}, labels)
}

func counter(subsystem, name, help string, labels ...string) *settableCounterVec {
	desc := prometheus.NewDesc(
		prometheus.BuildFQName("mesos", subsystem, name),
		help,
		labels,
		prometheus.Labels{},
	)

	return &settableCounterVec{
		desc:   desc,
		values: nil,
	}
}

type authInfo struct {
	username string
	password string
}

type httpClient struct {
	http.Client
	url  string
	auth authInfo
}

type metricCollector struct {
	*httpClient
	metrics map[prometheus.Collector]func(metricMap, prometheus.Collector) error
}

func newMetricCollector(httpClient *httpClient, metrics map[prometheus.Collector]func(metricMap, prometheus.Collector) error) prometheus.Collector {
	return &metricCollector{httpClient, metrics}
}

func (httpClient *httpClient) fetchAndDecode(endpoint string, target interface{}) bool {
	url := strings.TrimSuffix(httpClient.url, "/") + endpoint
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		log.Printf("Error creating HTTP request to %s: %s", url, err)
		return false
	}
	if httpClient.auth.username != "" && httpClient.auth.password != "" {
		req.SetBasicAuth(httpClient.auth.username, httpClient.auth.password)
	}
	res, err := httpClient.Do(req)
	if err != nil {
		log.Printf("Error fetching %s: %s", url, err)
		errorCounter.Inc()
		return false
	}
	defer res.Body.Close()

	if err := json.NewDecoder(res.Body).Decode(&target); err != nil {
		log.Print("Error decoding response body from %s: %s", err)
		errorCounter.Inc()
		return false
	}

	return true
}

func (c *metricCollector) Collect(ch chan<- prometheus.Metric) {
	var m metricMap
	c.fetchAndDecode("/metrics/snapshot", &m)
	for cm, f := range c.metrics {
		if err := f(m, cm); err != nil {
			if err == notFoundInMap {
				ch := make(chan *prometheus.Desc, 1)
				cm.Describe(ch)
				log.Printf("Couldn't find fields required to update %s\n", <-ch)
			} else {
				log.Printf("Error extracting metric: %s", err)
			}
			errorCounter.Inc()
			continue
		}
		cm.Collect(ch)
	}
}

func (c *metricCollector) Describe(ch chan<- *prometheus.Desc) {
	for m, _ := range c.metrics {
		m.Describe(ch)
	}
}
