package backend

import (
	"context"
	"errors"
	"os"
)

func (a Adapter) stopEnvironment(ctx context.Context, r InstallRequest, id string) (Evidence, error) {
	ev := Evidence{ResourceID: id, State: "unknown"}
	if err := validate(r); err != nil {
		return ev, err
	}
	if err := ctx.Err(); err != nil {
		return ev, err
	}
	if a.Runner == nil && os.Geteuid() == 0 {
		return ev, errors.New("ordinary user required")
	}
	if r.Recipe.Platform != "linux" {
		return a.stopBottles(ctx, r, id)
	}
	// Inspect validates the full container ID, managed name and ownership label.
	// Its display-only state is not used as proof that the environment stopped.
	if _, err := a.Inspect(ctx, r, id); err != nil {
		return ev, err
	}
	c := command("docker", "container", "stop", "-t", "10", id)
	c.SafeCancel = false
	c.Timeout = 0
	result, err := a.runner().Run(ctx, c)
	ev.Result = result
	if err != nil {
		return ev, err
	}
	// Once Docker accepted the mutation, retain verification even if the caller
	// requested cancellation. Completion is never guessed from CLI exit alone.
	checked, err := a.ConfirmStopped(context.WithoutCancel(ctx), r, id)
	checked.Result = result
	return checked, err
}
