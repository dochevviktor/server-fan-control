package config

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/dochevviktor/server-fan-control/internal/curve"
)

type Config struct {
	SchemaVersion    int                    `json:"schema_version"`
	Revision         uint64                 `json:"revision"`
	SamplingSeconds  int                    `json:"sampling_seconds"`
	FreshnessSeconds int                    `json:"freshness_seconds"`
	FailureLimit     int                    `json:"failure_limit"`
	Curves           map[string]curve.Curve `json:"curves"`
	GPUs             map[string]Policy      `json:"gpus"`
}
type Policy struct {
	Mode               string  `json:"mode"`
	Curve              string  `json:"curve"`
	Fans               []int   `json:"fans"`
	MinDuty            int     `json:"min_duty"`
	CriticalTemp       float64 `json:"critical_temp"`
	Hysteresis         float64 `json:"hysteresis"`
	CoolingHoldSeconds int     `json:"cooling_hold_seconds"`
	LimitsConfirmed    bool    `json:"limits_confirmed"`
}

func Default() Config {
	return Config{SchemaVersion: 1, Revision: 1, SamplingSeconds: 5, FreshnessSeconds: 15, FailureLimit: 3, Curves: map[string]curve.Curve{"Balanced": {Type: "linear", Points: []curve.Point{{Temperature: 30, Duty: 30}, {Temperature: 60, Duty: 50}, {Temperature: 80, Duty: 100}}}}, GPUs: map[string]Policy{}}
}
func Clone(c Config) Config {
	b, _ := json.Marshal(c)
	var out Config
	_ = json.Unmarshal(b, &out)
	return out
}
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func Validate(c Config) error {
	if c.SchemaVersion != 1 {
		return fmt.Errorf("unsupported schema_version %d", c.SchemaVersion)
	}
	if c.SamplingSeconds < 1 || c.SamplingSeconds > 60 {
		return fmt.Errorf("sampling_seconds must be 1..60")
	}
	if c.FreshnessSeconds < c.SamplingSeconds || c.FreshnessSeconds > 300 {
		return fmt.Errorf("freshness_seconds must be between sampling_seconds and 300")
	}
	if c.FailureLimit < 1 || c.FailureLimit > 10 {
		return fmt.Errorf("failure_limit must be 1..10")
	}
	if c.Curves == nil || c.GPUs == nil || len(c.Curves) > 128 || len(c.GPUs) > 128 {
		return fmt.Errorf("curves and gpus must be objects with at most 128 entries")
	}
	for name, cv := range c.Curves {
		if len(name) == 0 || len(name) > 80 || strings.TrimSpace(name) != name {
			return fmt.Errorf("invalid curve name")
		}
		if err := curve.Validate(cv); err != nil {
			return fmt.Errorf("curve %q: %w", name, err)
		}
	}
	for id, p := range c.GPUs {
		if !strings.HasPrefix(id, "GPU-") || len(id) > 100 || strings.ContainsAny(id, "/\\ \t\r\n") {
			return fmt.Errorf("invalid GPU UUID %q", id)
		}
		if p.Mode != "automatic" && p.Mode != "curve" {
			return fmt.Errorf("GPU %s mode must be automatic or curve", id)
		}
		cv, exists := c.Curves[p.Curve]
		if p.Curve != "" && !exists {
			return fmt.Errorf("GPU %s references missing curve", id)
		}
		seen := map[int]bool{}
		for _, fan := range p.Fans {
			if fan < 0 || fan > 63 || seen[fan] {
				return fmt.Errorf("GPU %s has invalid or duplicate fan indices", id)
			}
			seen[fan] = true
		}
		if p.MinDuty < 0 || p.MinDuty > 100 || !finite(p.CriticalTemp) || p.CriticalTemp < 0 || p.CriticalTemp > 150 || !finite(p.Hysteresis) || p.Hysteresis < 0 || p.Hysteresis > 20 || p.CoolingHoldSeconds < 0 || p.CoolingHoldSeconds > 300 {
			return fmt.Errorf("GPU %s has invalid thermal limits or cooling settings", id)
		}
		if p.Mode == "curve" {
			if !exists || curve.MaxDuty(cv) != 100 {
				return fmt.Errorf("GPU %s needs a curve with a 100%% upper endpoint", id)
			}
			if !p.LimitsConfirmed || p.CriticalTemp <= 0 {
				return fmt.Errorf("GPU %s requires confirmed hardware duty floor and critical temperature", id)
			}
		}
	}
	return nil
}
