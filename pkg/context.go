package waylog

import (
	"context"
	"sync"
	"time"
)

type RequestState struct {
	start             time.Time
	statusCode        int
	err               error
	callerService     string
	downstreamService string
	serviceName       string
	user              User
	flow              string
	flags             []string
	httpMethod        string
	routeTemplate     string
	once              sync.Once
	mu                sync.Mutex
}

type requestStateKey struct{}

type userKey struct{}

type flowKey struct{}

type flagsKey struct{}

type httpMethodKey struct{}

type routeTemplateKey struct{}

func WithRequestState(ctx context.Context, state *RequestState) context.Context {
	return context.WithValue(ctx, requestStateKey{}, state)
}

func requestStateFromContext(ctx context.Context) (*RequestState, bool) {
	state, ok := ctx.Value(requestStateKey{}).(*RequestState)
	return state, ok
}

func RequestStateFromContext(ctx context.Context) (*RequestState, bool) {
	return requestStateFromContext(ctx)
}

func NewRequestState(start time.Time, statusCode int, callerService string, serviceName string) *RequestState {
	return &RequestState{
		start:         start,
		statusCode:    statusCode,
		callerService: callerService,
		serviceName:   serviceName,
	}
}

func (s *RequestState) SetStatus(statusCode int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statusCode = statusCode
}

func (s *RequestState) SetDownstream(service string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.downstreamService = service
}

func (s *RequestState) ServiceName() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.serviceName
}

func WithUser(ctx context.Context, user User) context.Context {
	if user.ID == "" {
		user.ID = "system"
	}
	if state, ok := requestStateFromContext(ctx); ok && state != nil {
		state.SetUser(user)
		return ctx
	}
	return context.WithValue(ctx, userKey{}, user)
}

func userFromContext(ctx context.Context) (User, bool) {
	if state, ok := requestStateFromContext(ctx); ok && state != nil {
		return state.User()
	}
	user, ok := ctx.Value(userKey{}).(User)
	return user, ok
}

func WithFlow(ctx context.Context, flow string) context.Context {
	if state, ok := requestStateFromContext(ctx); ok && state != nil {
		state.SetFlow(flow)
		return ctx
	}
	return context.WithValue(ctx, flowKey{}, flow)
}

func flowFromContext(ctx context.Context) (string, bool) {
	if state, ok := requestStateFromContext(ctx); ok && state != nil {
		return state.Flow()
	}
	flow, ok := ctx.Value(flowKey{}).(string)
	return flow, ok
}

func WithFlags(ctx context.Context, flags []string) context.Context {
	copied := make([]string, len(flags))
	copy(copied, flags)
	if state, ok := requestStateFromContext(ctx); ok && state != nil {
		state.SetFlags(copied)
		return ctx
	}
	return context.WithValue(ctx, flagsKey{}, copied)
}

func flagsFromContext(ctx context.Context) ([]string, bool) {
	if state, ok := requestStateFromContext(ctx); ok && state != nil {
		return state.Flags()
	}
	flags, ok := ctx.Value(flagsKey{}).([]string)
	return flags, ok
}

func (s *RequestState) SetUser(user User) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if user.ID == "" {
		user.ID = "system"
	}
	s.user = user
}

func (s *RequestState) User() (User, bool) {
	if s == nil {
		return User{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.user, s.user.ID != ""
}

func (s *RequestState) SetFlow(flow string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flow = flow
}

func (s *RequestState) Flow() (string, bool) {
	if s == nil {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flow, s.flow != ""
}

func (s *RequestState) SetFlags(flags []string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flags = make([]string, len(flags))
	copy(s.flags, flags)
}

func (s *RequestState) Flags() ([]string, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.flags) == 0 {
		return nil, false
	}
	copied := make([]string, len(s.flags))
	copy(copied, s.flags)
	return copied, true
}

func (s *RequestState) SetHTTPMethod(method string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.httpMethod = method
}

func (s *RequestState) HTTPMethod() (string, bool) {
	if s == nil {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.httpMethod, s.httpMethod != ""
}

func (s *RequestState) SetRouteTemplate(rt string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routeTemplate = rt
}

func (s *RequestState) RouteTemplate() (string, bool) {
	if s == nil {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.routeTemplate, s.routeTemplate != ""
}

func WithHTTPMethod(ctx context.Context, method string) context.Context {
	if state, ok := requestStateFromContext(ctx); ok && state != nil {
		state.SetHTTPMethod(method)
		return ctx
	}
	return context.WithValue(ctx, httpMethodKey{}, method)
}

func httpMethodFromContext(ctx context.Context) (string, bool) {
	if state, ok := requestStateFromContext(ctx); ok && state != nil {
		return state.HTTPMethod()
	}
	m, ok := ctx.Value(httpMethodKey{}).(string)
	return m, ok
}

func WithRouteTemplate(ctx context.Context, rt string) context.Context {
	if state, ok := requestStateFromContext(ctx); ok && state != nil {
		state.SetRouteTemplate(rt)
		return ctx
	}
	return context.WithValue(ctx, routeTemplateKey{}, rt)
}

func routeTemplateFromContext(ctx context.Context) (string, bool) {
	if state, ok := requestStateFromContext(ctx); ok && state != nil {
		return state.RouteTemplate()
	}
	rt, ok := ctx.Value(routeTemplateKey{}).(string)
	return rt, ok
}
