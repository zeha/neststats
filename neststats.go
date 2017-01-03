package main

import (
	"encoding/json"
	"flag"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httputil"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type ThermostatData struct {
	CurrentHumidity    float64 `json:"humidity"`
	CurrentTemperature float64 `json:"ambient_temperature_c"`
	TargetTemperature  float64 `json:"target_temperature_c"`
	HvacState          string  `json:"hvac_state"`
	StructureID        string  `json:"structure_id"`
}

type StampedThermostatData struct {
	Stamp time.Time      `json:"stamp"`
	Data  ThermostatData `json:"data"`
}

var currentData ThermostatData
var currentDataTime time.Time
var currentDataMutex sync.Mutex

var (
	promHumidity = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "env_humidity",
		Help: "Current humidity.",
	})
	promTemperature = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "env_temperature",
		Help: "Current temperature.",
	})
	promTargetTemperature = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "target_temperature",
		Help: "Target temperature.",
	})
	promIsHeating = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "is_heating",
		Help: "Flag (0 or 1) indicating if currently heating.",
	})
)

func init() {
	prometheus.MustRegister(promHumidity)
	prometheus.MustRegister(promTemperature)
	prometheus.MustRegister(promTargetTemperature)
	prometheus.MustRegister(promIsHeating)
}

func headerAdder(auth string) func(req *http.Request) {
	return func(req *http.Request) {
		req.Header.Add("Content-Type", "application/json")
		req.Header.Add("Authorization", auth)
		req.Header.Add("User-Agent", "curl/7.51.0")
	}
}

func checkRedirectFunc(headerAdder func(*http.Request)) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		// re-add Authorization etc.
		headerAdder(req)
		// debug(httputil.DumpRequestOut(req, true))
		return nil
	}
}

func download(thermostatID string, clientSecret string) (ThermostatData, error) {
	var data ThermostatData

	auth := "Bearer " + clientSecret
	myHeaderAdder := headerAdder(auth)

	req, err := http.NewRequest("GET", "https://developer-api.nest.com/devices/thermostats/"+thermostatID, nil)

	client := &http.Client{
		CheckRedirect: checkRedirectFunc(myHeaderAdder),
	}

	if err != nil {
		return data, err
	}
	myHeaderAdder(req)

	debug(httputil.DumpRequestOut(req, true))

	resp, err := client.Do(req)
	if err != nil {
		return data, err
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return data, err
	}

	if *doDebug {
		log.Printf("json: %s", body)
	}

	json.Unmarshal(body, &data)
	return data, nil
}

func downloadAndStore(thermostatID string, clientSecret string) {
	ts, err := download(thermostatID, clientSecret)
	if err != nil {
		log.Printf("error: %v", err)
	} else {
		if *doDebug {
			log.Printf("%v", ts)
		}
		currentDataMutex.Lock()
		currentData = ts
		currentDataTime = time.Now()
		currentDataMutex.Unlock()
		promHumidity.Set(ts.CurrentHumidity)
		promTemperature.Set(ts.CurrentTemperature)
		promTargetTemperature.Set(ts.TargetTemperature)
		var isHeating float64
		if ts.HvacState == "heating" {
			isHeating = 1
		} else {
			isHeating = 0
		}
		promIsHeating.Set(isHeating)
	}
}

var listenOn = flag.String("listen-address", "127.0.0.1:9092", "The address to listen on for HTTP requests.")
var clientSecret = flag.String("client-secret", "", "")
var thermostatID = flag.String("thermostat-id", "", "")
var doDebug = flag.Bool("debug", false, "emit debug info")

func main() {
	flag.Parse()
	if *clientSecret == "" || *thermostatID == "" {
		log.Fatal("clientSecret or thermostatID missing\n")
	}
	log.Printf("starting, will listen on %v", *listenOn)

	downloadAndStore(*thermostatID, *clientSecret)

	ticker := time.NewTicker(time.Second * 30)
	go func() {
		for t := range ticker.C {
			log.Printf("tick at %v", t)
			downloadAndStore(*thermostatID, *clientSecret)
		}
	}()

	http.HandleFunc("/data", httpDataHandler)
	http.Handle("/metrics", promhttp.Handler())
	log.Fatal(http.ListenAndServe(*listenOn, nil))
}

func httpDataHandler(w http.ResponseWriter, req *http.Request) {
	var data StampedThermostatData
	currentDataMutex.Lock()
	data.Data = currentData
	data.Stamp = currentDataTime
	currentDataMutex.Unlock()

	b, _ := json.Marshal(data)
	w.Write(b)
}

func debug(data []byte, err error) {
	if err == nil {
		if *doDebug {
			log.Printf("%s\n\n", data)
		}
	} else {
		log.Fatalf("%s\n\n", err)
	}
}
