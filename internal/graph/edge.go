package graph

type EdgeType string

const (
	EdgeRequestBy  EdgeType = "request_by"
	EdgeHandledBy  EdgeType = "handled_by"
	EdgeUsedFlag   EdgeType = "used_flag"
	EdgeFailedWith EdgeType = "failed_with"
	EdgeCalls      EdgeType = "calls"

)

type Edge struct {
	From string
	To   string
	Type EdgeType
}
