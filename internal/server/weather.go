package server

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"time"

	"cricketstream/internal/pyval"
)

// Fallback coordinates only — /match/fetch saves the real ground's weather_lat/lon from
// PlayCricket into state, and those win. Without that preference every club got this
// ground's weather (and the DLS rain threshold keyed off it).
const groundLat, groundLon = 50.691, -4.093

func groundCoords() (float64, float64) {
	cfg := LoadState()
	lat, lon := groundLat, groundLon
	if pyval.Truthy(cfg["weather_lat"]) {
		f, err := pyval.Float(cfg["weather_lat"])
		if err != nil {
			return groundLat, groundLon
		}
		lat = f
	}
	if pyval.Truthy(cfg["weather_lon"]) {
		f, err := pyval.Float(cfg["weather_lon"])
		if err != nil {
			return groundLat, groundLon
		}
		lon = f
	}
	return lat, lon
}

var wmoIcons = map[int]string{0: "☀", 1: "🌤", 2: "⛅", 3: "☁", 45: "🌫", 48: "🌫", 51: "🌦", 53: "🌦", 55: "🌧", 61: "🌧", 63: "🌧", 65: "🌧", 71: "🌨", 73: "🌨", 75: "❄", 80: "🌦", 81: "🌧", 82: "⛈", 95: "⛈", 96: "⛈", 99: "⛈"}
var wmoDesc = map[int]string{0: "Clear sky", 1: "Mainly clear", 2: "Partly cloudy", 3: "Overcast", 45: "Foggy", 48: "Freezing fog", 51: "Light drizzle", 53: "Drizzle", 55: "Heavy drizzle", 61: "Light rain", 63: "Rain", 65: "Heavy rain", 71: "Light snow", 73: "Snow", 75: "Heavy snow", 80: "Rain showers", 81: "Heavy showers", 82: "Violent showers", 95: "Thunderstorm", 96: "Thunderstorm+hail", 99: "Heavy thunderstorm"}

// httpGetJSON fetches a JSON document with a User-Agent and timeout.
func httpGetJSON(u string, timeout time.Duration, headers map[string]string) (M, error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "CricketStreamOverlay/1.0")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	c := &http.Client{Timeout: timeout}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, &httpError{Code: resp.StatusCode, Body: string(body)}
	}
	var out M
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// httpError is a non-2xx answer (Python's urllib.error.HTTPError).
type httpError struct {
	Code int
	Body string
}

func (e *httpError) Error() string { return fmt.Sprintf("HTTP Error %d", e.Code) }

func pyRoundInt(x float64) int { return int(math.RoundToEven(x)) }

// FetchWeatherData is the ground's weather now and the rain risk for the next hours.
func FetchWeatherData() M {
	lat, lon := groundCoords()
	q := url.Values{}
	q.Set("latitude", pyval.Str(lat))
	q.Set("longitude", pyval.Str(lon))
	q.Set("current", "temperature_2m,relative_humidity_2m,wind_speed_10m,weather_code")
	q.Set("hourly", "precipitation_probability")
	q.Set("forecast_hours", "4")
	q.Set("minutely_15", "precipitation") // when rain starts or stops (rainOutlook)
	q.Set("forecast_minutely_15", "13")
	q.Set("wind_speed_unit", "mph")
	q.Set("timezone", "Europe/London")
	data, err := weatherGet("https://api.open-meteo.com/v1/forecast?"+q.Encode(), 8*time.Second, nil)
	if err != nil {
		return M{"ok": false, "error": err.Error()}
	}
	cur, ok := data["current"].(map[string]any)
	if !ok {
		return M{"ok": false, "error": "'current'"}
	}
	code := gi(cur, "weather_code", 0)
	// Rain risk = worst hourly precipitation probability over the next ~3 hours. Drives
	// the automatic DLS par display in the overlay.
	rain := 0.0
	probs := gl(gm(data, "hourly"), "precipitation_probability")
	for i, p := range probs {
		if i >= 4 || p == nil {
			continue
		}
		if f, err := pyval.Float(p); err == nil && f > rain {
			rain = f
		}
	}
	icon, ok := wmoIcons[code]
	if !ok {
		icon = "?"
	}
	desc, ok := wmoDesc[code]
	if !ok {
		desc = "Unknown"
	}
	out := M{"ok": true, "temp": pyRoundInt(gf(cur, "temperature_2m", 0)),
		"humidity":  pyRoundInt(gf(cur, "relative_humidity_2m", 0)),
		"wind":      fmt.Sprintf("%d mph", pyRoundInt(gf(cur, "wind_speed_10m", 0))),
		"rain_prob": int(rain), "icon": icon, "description": desc}
	// The next three hours in quarter-hours: the overlay's "rain approaching" strap and
	// whether the pre-match and break shows put the radar up.
	m15 := gm(data, "minutely_15")
	for k, v := range outlookFrom(gl(m15, "time"), gl(m15, "precipitation")) {
		out[k] = v
	}
	return out
}
