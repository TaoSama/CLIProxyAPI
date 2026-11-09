package config

import (
	"gopkg.in/yaml.v3"
	"testing"
)

func TestRouteMetadataLogV8Config(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		raw := "observability:\n  logs:\n    route-metadata-log: false\n"
		if enabled {
			raw = "observability:\n  logs:\n    route-metadata-log: true\n"
		}
		cfg, err := ParseConfigBytes([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.RouteMetadataLog != enabled || cfg.Debug || cfg.RequestLog {
			t.Fatal("route logging changed other diagnostics")
		}
		data, errMarshal := yaml.Marshal(cfg)
		if errMarshal != nil {
			t.Fatal(errMarshal)
		}
		after, errAfter := ParseConfigBytes(data)
		if errAfter != nil {
			t.Fatal(errAfter)
		}
		if after.RouteMetadataLog != enabled {
			t.Fatal("route metadata flag lost")
		}
	}
	cfg, err := ParseConfigBytes([]byte("port: 8317\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RouteMetadataLog {
		t.Fatal("enabled by default")
	}
}
