package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaultsAndRejectInvalidInput(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		valid      bool
	}{
		{"defaults", `{"hosts":[{"name":"dev","ssh":"user@dev"}]}`, true},
		{"unknown", `{"hosts":[{"name":"dev","ssh":"dev"}],"typo":true}`, false},
		{"option injection", `{"hosts":[{"name":"dev","ssh":"-oProxyCommand=bad"}]}`, false},
		{"shell injection", `{"hosts":[{"name":"dev","ssh":"user@host;echo bad"}]}`, false},
		{"duplicate host", `{"hosts":[{"name":"dev","ssh":"a"},{"name":"dev","ssh":"b"}]}`, false},
		{"unbounded", `{"hosts":[{"name":"dev","ssh":"dev"}],"max_services_per_host":999999}`, false},
		{"busy poll", `{"hosts":[{"name":"dev","ssh":"dev"}],"poll_interval":"1ms"}`, false},
		{"trailing object", `{"hosts":[{"name":"dev","ssh":"dev"}]} {}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(tt.body), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := Load(path)
			if (err == nil) != tt.valid {
				t.Fatalf("valid=%v, error=%v", tt.valid, err)
			}
			if tt.name == "defaults" && (c.Hosts[0].Discovery != "all" || !c.Hosts[0].Automatic() || len(c.Hosts[0].ExcludePorts) != 1 || c.Hosts[0].ExcludePorts[0] != 22) {
				t.Fatalf("wrong defaults: %+v", c)
			}
		})
	}
}
