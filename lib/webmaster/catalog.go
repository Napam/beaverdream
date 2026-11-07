package webmaster

import "encoding/json"

// ToolInfo is one tool the webmaster can call, as the model sees it. The admin
// panel reads this list, so it comes from the same definitions the requests use.
type ToolInfo struct {
	Name        string
	Description string
	// Parameters is the request schema as indented JSON, for reading.
	Parameters string
}

// Tools lists the mutation tools in the order a request advertises them.
func Tools() []ToolInfo {
	var out []ToolInfo
	for _, tool := range toolParams() {
		fn := tool.OfFunction
		if fn == nil {
			continue
		}
		parameters, err := json.MarshalIndent(fn.Function.Parameters, "", "  ")
		if err != nil {
			parameters = nil
		}
		out = append(out, ToolInfo{
			Name:        fn.Function.Name,
			Description: fn.Function.Description.Or(""),
			Parameters:  string(parameters),
		})
	}
	return out
}
