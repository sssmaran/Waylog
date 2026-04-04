package tools

import "github.com/sssmaran/WaylogCLI/internal/tracestore"

func traceStoreFrom(store Store) *tracestore.Store {
	if store == nil {
		return nil
	}
	return store.TraceStore()
}
