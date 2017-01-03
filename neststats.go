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

type StampedData struct {
	ThermostatStamp time.Time      `json:"thermostatStamp"`
	ThermostatData  ThermostatData `json:"thermostatData"`
	WeatherStamp    time.Time      `json:"weatherStamp"`
	WeatherData     OwmWeatherMain `json:"weatherData"`
}

type OwmWeatherMain struct {
	Temperature float64 `json:"temp"`
	Pressure    float64 `json:"pressure"`
	Humidity    float64 `json:"humidity"`
}

type OwmResult struct {
	WeatherMain OwmWeatherMain `json:"main"`
	// {"coord": {"lon":16.37,"lat":48.21},
	// 	"weather":[
	// 		{"id":800,"main":"Clear","description":"clear sky","icon":"01n"}
	// 	],
	// 	"base":"stations",
	// 	"main": {"temp":275.15,"pressure":1018,"humidity":55,"temp_min":275.15,"temp_max":275.15},
	// 	"visibility": 10000,
	//  "wind":{"speed":4.6,"deg":240},
	//  "clouds":{"all":0},
	//  "dt":1483482600,
	//  "sys":{"type":1,"id":5934,"message":0.0133,"country":"AT","sunrise":1483425896,"sunset":1483456460},
	//  "id":2761369,"name":"Vienna","cod":200}
}

var currentData ThermostatData
var currentDataTime time.Time
var currentWeather OwmWeatherMain
var currentWeatherTime time.Time
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
	promOutsideHumidity = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "outside_humidity",
		Help: "Current humidity (outside).",
	})
	promOutsideTemperature = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "outside_temperature",
		Help: "Current temperature (outside).",
	})
	promOutsidePressure = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "outside_pressure",
		Help: "Current pressure (outside).",
	})
)

func init() {
	prometheus.MustRegister(promHumidity)
	prometheus.MustRegister(promTemperature)
	prometheus.MustRegister(promTargetTemperature)
	prometheus.MustRegister(promIsHeating)

	prometheus.MustRegister(promOutsideHumidity)
	prometheus.MustRegister(promOutsideTemperature)
	prometheus.MustRegister(promOutsidePressure)
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

func downloadNest(thermostatID string, clientSecret string) (ThermostatData, error) {
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

func downloadNestAndStore(thermostatID string, clientSecret string) {
	ts, err := downloadNest(thermostatID, clientSecret)
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

func downloadWeatherAndStore(apiKey string, cityID string) {
	var result OwmResult
	resp, err := http.Get("http://api.openweathermap.org/data/2.5/weather?units=metric&id=" + cityID + "&appid=" + apiKey)

	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Printf("error: %v", err)
		return
	}

	if *doDebug {
		log.Printf("json: %s", body)
	}

	json.Unmarshal(body, &result)

	if err != nil {
		log.Printf("error: %v", err)
	} else {
		if *doDebug {
			log.Printf("%v", result)
		}
		currentDataMutex.Lock()
		currentWeather = result.WeatherMain
		currentWeatherTime = time.Now()
		currentDataMutex.Unlock()
		promOutsideHumidity.Set(result.WeatherMain.Humidity)
		promOutsideTemperature.Set(result.WeatherMain.Temperature)
		promOutsidePressure.Set(result.WeatherMain.Pressure)
	}
}

var listenOn = flag.String("listen-address", "127.0.0.1:9092", "The address to listen on for HTTP requests.")
var clientSecret = flag.String("client-secret", "", "")
var thermostatID = flag.String("thermostat-id", "", "")
var doDebug = flag.Bool("debug", false, "emit debug info")
var owmAPIKey = flag.String("owm-apikey", "", "openweathermap API Key")
var owmCityID = flag.String("owm-city-id", "2761369", "openweathermap.org cityID") // cityID defaults to Vienna, AT

func main() {
	flag.Parse()
	if *clientSecret == "" || *thermostatID == "" {
		log.Fatal("clientSecret or thermostatID missing\n")
	}
	log.Printf("starting, will listen on %v", *listenOn)

	nestTicker := time.NewTicker(time.Second * 30)
	go func() {
		downloadNestAndStore(*thermostatID, *clientSecret)
		for t := range nestTicker.C {
			log.Printf("nestTicker tick at %v", t)
			downloadNestAndStore(*thermostatID, *clientSecret)
		}
	}()

	weatherTicker := time.NewTicker(time.Minute * 30)
	go func() {
		if *owmAPIKey == "" {
			log.Printf("no OWM Api Key, not fetching weather data")
			return
		}
		downloadWeatherAndStore(*owmAPIKey, *owmCityID)
		for t := range weatherTicker.C {
			log.Printf("weatherTicker tick at %v", t)
			downloadWeatherAndStore(*owmAPIKey, *owmCityID)
		}
	}()

	http.HandleFunc("/data", httpDataHandler)
	http.Handle("/metrics", promhttp.Handler())
	log.Fatal(http.ListenAndServe(*listenOn, nil))
}

func httpDataHandler(w http.ResponseWriter, req *http.Request) {
	var data StampedData
	currentDataMutex.Lock()
	data.ThermostatData = currentData
	data.ThermostatStamp = currentDataTime
	data.WeatherData = currentWeather
	data.WeatherStamp = currentWeatherTime
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
