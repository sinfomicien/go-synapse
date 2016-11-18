package synapse

import (
	"encoding/json"
	"github.com/n0rad/go-erlog/data"
	"github.com/n0rad/go-erlog/errs"
	"github.com/n0rad/go-erlog/logs"
	"sync"
	"time"
)

type RouterCommon struct {
	Type                        string
	EventsBufferDurationInMilli int
	Services                    []*Service

	synapse    *Synapse
	lastEvents map[string]*ServiceReport
	fields     data.Fields
}

type Router interface {
	Init(s *Synapse) error
	getFields() data.Fields
	Run(context *ContextImpl)
	Update(serviceReports []ServiceReport) error
	GetService(name string) (*Service, error)
	ServicesNames() []string
	ParseServerOptions(data []byte) (interface{}, error)
	ParseRouterOptions(data []byte) (interface{}, error)
}

func (r *RouterCommon) ServicesNames() []string {
	keys := make([]string, len(r.Services))

	i := 0
	for _, service := range r.Services {
		keys[i] = service.Name
		i++
	}
	return keys
}

func (r *RouterCommon) commonInit(router Router, synapse *Synapse) error {
	r.fields = data.WithField("type", r.Type)
	r.synapse = synapse

	if r.EventsBufferDurationInMilli == 0 {
		r.EventsBufferDurationInMilli = 500
	}

	r.lastEvents = make(map[string]*ServiceReport)
	for _, service := range r.Services {
		if err := service.Init(router, synapse); err != nil {
			return errs.WithEF(err, r.fields, "Failed to init service")
		}
	}

	return nil
}

func (r *RouterCommon) RunCommon(context *ContextImpl, router Router) {
	context.doneWaiter.Add(1)
	defer context.doneWaiter.Done()

	events := make(chan ServiceReport)
	watcherContext := newContext(context.oneshot)
	for _, service := range r.Services {
		go service.typedWatcher.Watch(watcherContext, events, service)
	}

	go r.eventsProcessor(events, router)

	<-context.stop
	close(watcherContext.stop)
	watcherContext.doneWaiter.Wait()
	logs.WithF(r.fields).Debug("All Watchers stopped")
	close(events)
}

func (r *RouterCommon) eventsProcessor(events chan ServiceReport, router Router) {
	updateMutex := sync.Mutex{}
	bufEvents := make(map[string]ServiceReport)
	var eventsTimer *time.Timer

	deferRun := func() {
		logs.WithF(r.fields.WithField("events", bufEvents)).Debug("Run events buffer")
		updateMutex.Lock()
		reports := []ServiceReport{}
		for _, s := range bufEvents {
			reports = append(reports, s)
		}
		bufEvents = make(map[string]ServiceReport)
		updateMutex.Unlock()

		r.handleReport(reports, router)
	}

	for {
		select {
		case event, ok := <-events:
			if !ok {
				return
			}

			logs.WithF(r.fields.WithField("event", event)).Debug("Router received an event")
			if eventsTimer != nil && !eventsTimer.Stop() {
				logs.WithF(r.fields.WithField("event", event)).Trace("Event Already fired")
			} else {
				logs.WithF(r.fields.WithField("event", event)).Trace("Event Added to buffer")
			}

			updateMutex.Lock()
			bufEvents[event.Service.Name] = event
			updateMutex.Unlock()
			eventsTimer = time.AfterFunc(time.Duration(r.EventsBufferDurationInMilli)*time.Millisecond, deferRun)
		}
	}
}

func (r *RouterCommon) handleReport(events []ServiceReport, router Router) {
	validEvents := []ServiceReport{}

	for _, event := range events {
		event.Service.ServerSort.Sort(&event.Reports)

		available, unavailable := event.AvailableUnavailable()
		r.synapse.serviceAvailableCount.WithLabelValues(event.Service.Name).Set(float64(available))
		r.synapse.serviceUnavailableCount.WithLabelValues(event.Service.Name).Set(float64(unavailable))

		if !event.HasActiveServers() {
			if r.lastEvents[event.Service.Name] == nil {
				logs.WithF(event.Service.fields).Warn("First Report has no active server. Not declaring in router")
			} else {
				logs.WithF(event.Service.fields).Error("Receiving report with no active server. Keeping previous report")
			}
			continue
		} else if r.lastEvents[event.Service.Name] == nil || r.lastEvents[event.Service.Name].HasActiveServers() != event.HasActiveServers() {
			logs.WithF(event.Service.fields.WithField("event", event)).Info("Server(s) available for router")
		}

		validEvents = append(validEvents, r.FilterCorrelations(event, events))
	}

	if len(validEvents) == 0 {
		logs.WithF(r.fields).Debug("Nothing to update on router")
		return
	}

	if err := router.Update(validEvents); err != nil {
		r.synapse.routerUpdateFailures.WithLabelValues(r.Type).Inc()
		logs.WithEF(err, r.fields).Error("Failed to report watch modification")
	}

	for i, e := range validEvents {
		e.Service.reported = true
		r.lastEvents[e.Service.Name] = &validEvents[i]
	}

}

func (r *RouterCommon) FilterCorrelations(current ServiceReport, serviceReports []ServiceReport) ServiceReport {
	if current.Service.ServerCorrelation.OtherServiceName == "" {
		return current
	}

	filtered := r.FilterCorrelation(current, r.lastEvents[current.Service.ServerCorrelation.OtherServiceName])
	for _, SameUpdateReports2 := range serviceReports {
		if current.Service.ServerCorrelation.OtherServiceName == SameUpdateReports2.Service.Name {
			logs.WithF(r.fields.WithField("current", current)).Debug("Found other correlation service in same report")
			filtered = r.FilterCorrelation(current, &SameUpdateReports2)
			break
		}
	}
	return filtered
}

func (r *RouterCommon) FilterCorrelation(reports ServiceReport, otherServiceReport *ServiceReport) ServiceReport {
	if otherServiceReport == nil {
		return reports
	}

	otherServiceReport.Service.ServerSort.Sort(&otherServiceReport.Reports)

	res := []Report{}
	for _, report := range reports.Reports {
		// TODO support other filter
		if len(otherServiceReport.Reports) > 0 && report.Name == otherServiceReport.Reports[0].Name {
			logs.WithF(r.fields.WithField("server", report.Name)).Debug("Removing correlated server")
			continue
		}
		res = append(res, report)
	}
	return ServiceReport{reports.Service, res}
}

func (r *RouterCommon) getFields() data.Fields {
	return r.fields
}

func RouterFromJson(content []byte, s *Synapse) (Router, error) {
	t := &RouterCommon{}
	if err := json.Unmarshal([]byte(content), t); err != nil {
		return nil, errs.WithE(err, "Failed to unmarshall check type")
	}

	fields := data.WithField("type", t.Type)
	var typedRouter Router
	switch t.Type {
	case "console":
		typedRouter = NewRouterConsole()
	case "haproxy":
		typedRouter = NewRouterHaProxy()
	case "template":
		typedRouter = NewRouterTemplate()
	default:
		return nil, errs.WithF(fields, "Unsupported router type")
	}

	if err := json.Unmarshal([]byte(content), &typedRouter); err != nil {
		return nil, errs.WithEF(err, fields, "Failed to unmarshall router")
	}

	if err := typedRouter.Init(s); err != nil {
		return nil, errs.WithEF(err, fields, "Failed to init router")
	}
	return typedRouter, nil
}

func (r *RouterCommon) GetService(name string) (*Service, error) {
	for _, s := range r.Services {
		if s.Name == name {
			return s, nil
		}
	}
	return nil, errs.WithF(r.fields.WithField("name", name), "Cannot found service with this name")

}
