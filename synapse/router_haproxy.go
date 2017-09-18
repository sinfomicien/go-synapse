package synapse

import (
	"bytes"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"github.com/n0rad/go-erlog/data"
	"github.com/n0rad/go-erlog/errs"
	"github.com/n0rad/go-erlog/logs"
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"text/template"
)

const PrometheusLabelSocketSuffix = "_socket"

type RouterHaProxy struct {
	RouterCommon
	HaProxyClient
	hapServerOptionsRegex *regexp.Regexp
}
type HapRouterOptions struct {
	Frontend []string
	Backend  []string
}
type HapServerOptionsTemplate struct {
	*template.Template
}

func NewRouterHaProxy() *RouterHaProxy {
	return &RouterHaProxy{}
}

func (r *RouterHaProxy) Run(context *ContextImpl) {
	r.RunCommon(context, r)
}

func (r *RouterHaProxy) Init(s *Synapse) error {

	if err := r.commonInit(r, s); err != nil {
		return errs.WithEF(err, r.RouterCommon.fields, "Failed to init common router")
	}
	if err := r.HaProxyClient.Init(); err != nil {
		return errs.WithEF(err, r.RouterCommon.fields, "Failed to init haproxy client")
	}

	r.synapse.routerUpdateFailures.WithLabelValues(r.Type + PrometheusLabelSocketSuffix).Set(0)
	r.synapse.routerUpdateFailures.WithLabelValues(r.Type).Set(0)
	r.hapServerOptionsRegex = regexp.MustCompile(`disabled`)
	if r.ConfigPath == "" {
		return errs.WithF(r.RouterCommon.fields, "ConfigPath is required for haproxy router")
	}
	if len(r.ReloadCommand) == 0 {
		return errs.WithF(r.RouterCommon.fields, "ReloadCommand is required for haproxy router")
	}

	return nil
}

func (r *RouterHaProxy) isSocketUpdatable(report ServiceReport) bool {
	previous := r.lastEvents[report.Service.Name]
	if previous == nil {
		logs.WithF(r.RouterCommon.fields.WithField("previous", previous).
			WithField("current", report)).
			Debug("Report length is different")
		return false
	}

	for _, new := range report.Reports {
		weightOnly := false
		for _, old := range previous.Reports {
			if new.Port == old.Port &&
				new.Name == old.Name &&
				new.HaProxyServerOptions == old.HaProxyServerOptions {
				weightOnly = true
				break
			}
		}

		if !weightOnly {
			logs.WithF(r.RouterCommon.fields.WithField("server", new)).
				Debug("Server was not existing or options has changed")
			return false
		}
	}
	return true
}

func reportInSlice(original []Report, copy Report) bool {
	for _, i := range original {
		if i.Name == copy.Name {
			return true
		}
	}
	return false
}

func (r *RouterHaProxy) DiffWithOldReport(report ServiceReport) (ServiceReport, error) {
	previous := r.lastEvents[report.Service.Name]
	// Skip when previous reports is empty and if servers are added.
	if previous == nil || len(previous.Reports) < len(report.Reports) {
		return report, nil
	}
	for _, newReport := range previous.Reports {
		removedReport := reportInSlice(report.Reports, newReport)
		if !removedReport {
			oldAddReport := newReport
			logs.WithF(r.RouterCommon.fields.WithField("previous", newReport)).
				Debug("Missing report")
			*oldAddReport.Available = removedReport
			report.Reports = append(report.Reports, oldAddReport)
		}
	}
	return report, nil
}

func (r *RouterHaProxy) Update(serviceReports []ServiceReport) error {
	reloadNeeded := r.socketPath == ""
	for _, report := range serviceReports {
		updatedReport, err := r.DiffWithOldReport(report)
		if err != nil {
			return errs.WithEF(err, r.RouterCommon.fields.WithField("report", updatedReport), "Failed to update report with old nodes")
		}
		if !r.isSocketUpdatable(updatedReport) {
			reloadNeeded = true
		}
		r.lastEvents[report.Service.Name] = &updatedReport
		front, back, err := r.toFrontendAndBackend(updatedReport)
		if err != nil {
			return errs.WithEF(err, r.RouterCommon.fields.WithField("report", updatedReport), "Failed to prepare frontend and backend")
		}
		r.Frontend[updatedReport.Service.Name+"_"+strconv.Itoa(updatedReport.Service.id)] = front
		r.Backend[updatedReport.Service.Name+"_"+strconv.Itoa(updatedReport.Service.id)] = back
	}

	if reloadNeeded {
		if err := r.Reload(); err != nil {
			return errs.WithEF(err, r.RouterCommon.fields, "Failed to reload haproxy")
		}
	} else if err := r.SocketUpdate(); err != nil {
		r.synapse.routerUpdateFailures.WithLabelValues(r.Type + PrometheusLabelSocketSuffix).Inc()
		logs.WithEF(err, r.RouterCommon.fields).Error("Update by Socket failed. Reloading instead")
		if err := r.Reload(); err != nil {
			return errs.WithEF(err, r.RouterCommon.fields, "Failed to reload haproxy")
		}
	}
	return nil
}

func (r *RouterHaProxy) toFrontendAndBackend(report ServiceReport) ([]string, []string, error) {
	frontend := []string{}
	if report.Service.typedRouterOptions != nil {
		for _, option := range report.Service.typedRouterOptions.(HapRouterOptions).Frontend {
			frontend = append(frontend, option)
		}
	}
	frontend = append(frontend, "default_backend "+report.Service.Name+"_"+strconv.Itoa(report.Service.id))

	backend := []string{}
	if report.Service.typedRouterOptions != nil {
		for _, option := range report.Service.typedRouterOptions.(HapRouterOptions).Backend {
			backend = append(backend, option)
		}
	}

	var serverOptions HapServerOptionsTemplate
	if report.Service.typedServerOptions != nil {
		serverOptions = report.Service.typedServerOptions.(HapServerOptionsTemplate)
	}
	for _, report := range report.Reports {
		server, err := r.reportToHaProxyServer(report, serverOptions)
		if err != nil {
			return nil, nil, errs.WithEF(err, r.RouterCommon.fields.WithField("name", report.Name), "Failed to prepare backend for server")
		}
		backend = append(backend, server)
	}

	return frontend, backend, nil
}

func (r *RouterHaProxy) reportToHaProxyServer(report Report, serverOptions HapServerOptionsTemplate) (string, error) {
	var buffer bytes.Buffer
	buffer.WriteString("server ")
	buffer.WriteString(report.Name)
	buffer.WriteString(" ")
	buffer.WriteString(report.Host)
	buffer.WriteString(":")
	buffer.WriteString(strconv.Itoa(int(report.Port)))
	buffer.WriteString(" ")
	if report.Weight != nil {
		buffer.WriteString("weight ")
		buffer.WriteString(strconv.Itoa(int(*report.Weight)))
	}
	buffer.WriteString(" ")
	buffer.WriteString(report.HaProxyServerOptions)

	res, err := renderServerOptionsTemplate(report, serverOptions)
	if err != nil {
		return "", errs.WithEF(err, r.RouterCommon.fields, "Failed to teom")
	}
	buffer.WriteString(" ")
	buffer.WriteString(res)
	if *report.Available == false && !r.hapServerOptionsRegex.MatchString(report.HaProxyServerOptions) {
		buffer.WriteString(" ")
		buffer.WriteString("disabled")
	}
	if *report.Available == false {
		buffer.WriteString(" ")
		buffer.WriteString("#isDisabled")
	}
	return buffer.String(), nil
}

func renderServerOptionsTemplate(report Report, serverOptions HapServerOptionsTemplate) (string, error) {
	if serverOptions.Template == nil {
		return "", nil
	}
	var buff bytes.Buffer
	if err := serverOptions.Execute(&buff, struct {
		Name string
	}{
		Name: report.Name,
	}); err != nil {
		return "", errs.WithE(err, "Failed to template serverOptions")
	}
	res := buff.String()
	if strings.Contains(res, "<no value>") {
		return "", errs.WithF(data.WithField("content", res), "serverOption templating has <no value>")
	}
	return res, nil
}

func (r *RouterHaProxy) ParseServerOptions(data []byte) (interface{}, error) {
	if len(data) == 0 {
		return nil, nil
	}

	fields := r.RouterCommon.fields.WithField("content", string(data))
	var serversOptions string
	err := json.Unmarshal(data, &serversOptions)
	if err != nil {
		return nil, errs.WithEF(err, fields, "Failed to Unmarshal serverOptions")
	}

	template, err := template.New("serverOptions").Funcs(TemplateFunctions).Parse(serversOptions)
	if err != nil {
		return nil, errs.WithEF(err, fields, "Failed to parse serversOptions template")
	}
	return HapServerOptionsTemplate{template}, nil
}

func (r *RouterHaProxy) ParseRouterOptions(data []byte) (interface{}, error) {
	routerOptions := HapRouterOptions{}
	err := json.Unmarshal(data, &routerOptions)
	if err != nil {
		return nil, errs.WithEF(err, r.RouterCommon.fields.WithField("content", string(data)), "Failed to Unmarshal routerOptions")
	}
	return routerOptions, nil
}

const letterBytes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
const (
	letterIdxBits = 6                    // 6 bits to represent a letter index
	letterIdxMask = 1<<letterIdxBits - 1 // All 1-bits, as many as letterIdxBits
	letterIdxMax  = 63 / letterIdxBits   // # of letter indices fitting in 63 bits
)

func Sha1String(s string) string {
	h := sha1.New()
	h.Write([]byte(s))
	bs := h.Sum(nil)
	return fmt.Sprintf("%x", bs)
}

func RandString(n int) string {
	b := make([]byte, n)
	// A rand.Int63() generates 63 random bits, enough for letterIdxMax letters!
	for i, cache, remain := n-1, rand.Int63(), letterIdxMax; i >= 0; {
		if remain == 0 {
			cache, remain = rand.Int63(), letterIdxMax
		}
		if idx := int(cache & letterIdxMask); idx < len(letterBytes) {
			b[i] = letterBytes[idx]
			i--
		}
		cache >>= letterIdxBits
		remain--
	}

	return string(b)
}

var TemplateFunctions map[string]interface{}

func init() {
	TemplateFunctions = make(map[string]interface{})
	TemplateFunctions["randString"] = RandString
	TemplateFunctions["sha1String"] = Sha1String
}
