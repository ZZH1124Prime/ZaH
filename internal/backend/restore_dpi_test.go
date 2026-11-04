package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/ZZH1124Prime/ZaH/internal/process"
	"strings"
	"testing"
)

type restoreDPIRunner struct {
	id, key, value string
	calls          []string
}

func (f *restoreDPIRunner) Run(_ context.Context, c process.Command) (process.Result, error) {
	args := strings.Join(c.Args, "|")
	f.calls = append(f.calls, args)
	if strings.Contains(args, "--json|list|bottles") {
		return process.Result{Stdout: fmt.Sprintf(`{"%s":{"Parameters":{"%s":%s}}}`, name(f.id), f.key, f.value)}, nil
	}
	if strings.Contains(args, "|edit|") {
		f.value = strings.TrimPrefix(c.Args[len(c.Args)-1], f.key+":")
		return process.Result{}, nil
	}
	return process.Result{}, fmt.Errorf("unexpected command")
}
func TestRestoreBottlePreservesSupportedDPI(t *testing.T) {
	for _, value := range []string{"144", "192", "0", "95", "385", "144.5", `"144"`} {
		t.Run(value, func(t *testing.T) {
			r, _, _, _ := windowsRequest(t)
			f := &restoreDPIRunner{id: r.InstanceID, key: "custom_dpi", value: "96"}
			err := (Adapter{f}).restoreBottleParameters(context.Background(), r, PortableBottle{Parameters: map[string]json.RawMessage{"custom_dpi": json.RawMessage(value)}})
			valid := value == "144" || value == "192" || value == "0"
			if valid {
				if err != nil || f.value != value {
					t.Fatal(err, f.value)
				}
			} else {
				if err == nil || f.value != "96" {
					t.Fatal("invalid DPI changed target", err, f.value)
				}
			}
		})
	}
	r, _, _, _ := windowsRequest(t)
	f := &restoreDPIRunner{id: r.InstanceID, key: "unrecognized", value: "96"}
	if err := (Adapter{f}).restoreBottleParameters(context.Background(), r, PortableBottle{Parameters: map[string]json.RawMessage{"unrecognized": json.RawMessage("144")}}); err == nil {
		t.Fatal("accepted unknown numeric setting")
	}
}
