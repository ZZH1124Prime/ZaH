package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/ZZH1124Prime/ZaH/internal/process"
)

func TestRuntimeVerificationRejectsHOMEAndMountDrift(t *testing.T) {
	r := request(t)
	if e := os.Mkdir(r.PrivateHome, 0700); e != nil {
		t.Fatal(e)
	}
	id := strings.Repeat("a", 64)
	for _, mode := range []string{"valid", "home", "missing", "readonly", "foreign", "root"} {
		t.Run(mode, func(t *testing.T) {
			home := r.PrivateHome
			source := r.PrivateHome
			rw := true
			mounts := []map[string]any{{"Type": "bind", "Source": source, "Destination": r.PrivateHome, "RW": rw}}
			uid := strconv.Itoa(os.Geteuid())
			switch mode {
			case "home":
				home = "/other"
			case "missing":
				mounts = nil
			case "readonly":
				mounts[0]["RW"] = false
			case "foreign":
				mounts[0]["Source"] = "/other"
			case "root":
				uid = "0"
			}
			rows := []map[string]any{{"Id": id, "Name": "/zah-" + r.InstanceID, "Config": map[string]any{"Labels": map[string]string{"org.zah.instance": r.InstanceID}, "Env": []string{"HOME=" + home}}, "Mounts": mounts}}
			b, _ := json.Marshal(rows)
			fake := &scriptRunner{replies: []process.Result{{Stdout: string(b)}, {Stdout: uid}}}
			err := (Adapter{fake}).verifyRuntimeBindings(context.Background(), r, id)
			if mode == "valid" && err != nil {
				t.Fatal(err)
			}
			if mode != "valid" && err == nil {
				t.Fatal("accepted unsafe runtime")
			}
			if len(fake.calls) > 1 && (fake.calls[1].SafeCancel || fake.calls[1].Timeout != 0) {
				t.Fatal("first entry initialization must not inherit a cancellable probe timeout")
			}
			if len(fake.calls) > 1 && !strings.HasSuffix(fmt.Sprint(fake.calls[1].Args), "id -u]") {
				t.Fatalf("%+v", fake.calls)
			}
		})
	}
}
