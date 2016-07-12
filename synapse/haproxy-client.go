package synapse

import (
	"text/template"
	"github.com/n0rad/go-erlog/errs"
	"github.com/n0rad/go-erlog/data"
	"bufio"
	"bytes"
	"io/ioutil"
	"github.com/n0rad/go-erlog/logs"
	"github.com/blablacar/go-nerve/nerve"
	"os"
	"time"
)

const haProxyConfigurationTemplate = `# Handled by synapse. Do not modify it.
global
{{- range .Global}}
  {{.}}{{end}}

defaults
{{- range .Defaults}}
  {{.}}{{end}}

{{range $key, $element := .Listen}}
listen {{$key}}
{{- range $element}}
  {{.}}{{end}}
{{end}}
{{range $key, $element := .Frontend}}
frontend {{$key}}
{{- range $element}}
  {{.}}{{end}}
{{end}}
{{range $key, $element := .Backend}}
backend {{$key}}
{{- range $element}}
  {{.}}{{end}}
{{end}}

`

type HaProxyConfig struct {
	Global   []string
	Defaults []string
	Listen   map[string][]string
	Frontend map[string][]string
	Backend  map[string][]string
}

type HaProxyClient struct {
	HaProxyConfig
	ConfigPath               string
	//SocketPath           string
	ReloadCommand            []string
	ReloadMinIntervalInMilli int
	ReloadTimeoutInMilli     int
	StatePath                string

	lastReload               time.Time
	template                 *template.Template
	fields                   data.Fields
}

func (hap *HaProxyClient) Init() error {
	hap.fields = data.WithField("config", hap.ConfigPath)

	if hap.Listen == nil {
		hap.Listen = make(map[string][]string)
	}
	if hap.Frontend == nil {
		hap.Frontend = make(map[string][]string)
	}
	if hap.Backend == nil {
		hap.Backend = make(map[string][]string)
	}

	if hap.ReloadMinIntervalInMilli == 0 {
		hap.ReloadMinIntervalInMilli = 500
	}

	if hap.ReloadTimeoutInMilli == 0 {
		hap.ReloadTimeoutInMilli = 1000
	}

	tmpl, err := template.New("ha-proxy-config").Parse(haProxyConfigurationTemplate)
	if err != nil {
		return errs.WithEF(err, hap.fields, "Failed to parse haproxy config template")
	}
	hap.template = tmpl

	return nil
}

func (hap *HaProxyClient) Reload() error {
	logs.WithF(hap.fields).Debug("Reloading haproxy")
	env := append(os.Environ(), "HAP_CONFIG=" + hap.ConfigPath)

	waitDuration := hap.lastReload.Add(time.Duration(hap.ReloadMinIntervalInMilli)*time.Millisecond).Sub(time.Now())
	if waitDuration > 0 {
		logs.WithF(hap.fields.WithField("wait", waitDuration)).Debug("Reloading too fast")
		time.Sleep(waitDuration)
	}
	defer func() {
		hap.lastReload = time.Now()
	}()
	if err := nerve.ExecCommandFull(hap.ReloadCommand, env, hap.ReloadTimeoutInMilli); err != nil {
		return errs.WithEF(err, hap.fields, "Failed to reload haproxy")
	}
	return nil
}

func (hap *HaProxyClient) writeConfig() error {
	var b bytes.Buffer
	writer := bufio.NewWriter(&b)
	if err := hap.template.Execute(writer, hap); err != nil {
		return errs.WithEF(err, hap.fields, "Failed to temlate haproxy configuration file")
	}
	if err := writer.Flush(); err != nil {
		return errs.WithEF(err, hap.fields, "Failed to flush buffer")
	}

	templated := b.Bytes()
	if logs.IsTraceEnabled() {
		logs.WithF(hap.fields.WithField("templated", string(templated))).Trace("Templated configuration file")
	}
	if err := ioutil.WriteFile(hap.ConfigPath, templated, 0644); err != nil {
		return errs.WithEF(err, hap.fields, "Failed to write configuration file")
	}
	return nil
}
