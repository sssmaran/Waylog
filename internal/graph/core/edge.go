package core

type EdgeType string

const (
	EdgeRequestBy  EdgeType = "request_by"
	EdgeHandledBy  EdgeType = "handled_by"
	EdgeUsedFlag   EdgeType = "used_flag"
	EdgeFailedWith EdgeType = "failed_with"
	EdgeCalls      EdgeType = "calls"
	
	EdgeRequestHasSpan EdgeType = "has_span"      
	EdgeSpanChildOf    EdgeType = "span_child_of" 
	EdgeSpanOnService  EdgeType = "span_on"   
	

)

type Edge struct {
	From string
	To   string
	Type EdgeType
}
