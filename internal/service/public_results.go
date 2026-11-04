package service

import (
	"encoding/json"

	"github.com/ZZH1124Prime/ZaH/internal/migration"
)

// Public results carry review information and durable ownership evidence, not a
// second copy of every archived checksum or private catalog record. Complete
// metadata remains in the verified bundle. This keeps file-heavy exports from
// turning one task event into a multi-megabyte RPC frame.
func compactInspection(in migration.Inspection) migration.Inspection {
	var kind struct {
		Format string `json:"format"`
	}
	_ = json.Unmarshal(in.Catalog, &kind)
	in.Catalog, _ = json.Marshal(kind)
	in.Checksums = nil
	in.Bindings = nil
	return in
}
func compactExecutionResult(value any) any {
	switch v := value.(type) {
	case migration.Inspection:
		return compactInspection(v)
	case EnvironmentBundleResult:
		v.Inspection = compactInspection(v.Inspection)
		return v
	case WorkbenchBundleResult:
		v.Inspection = compactInspection(v.Inspection)
		results := make(map[string]EnvironmentBundleResult, len(v.Instances))
		for id, item := range v.Instances {
			item.Inspection = compactInspection(item.Inspection)
			results[id] = item
		}
		v.Instances = results
		return v
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, item := range v {
			out[k] = compactExecutionResult(item)
		}
		return out
	default:
		return value
	}
}
