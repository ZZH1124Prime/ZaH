package backend

import (
	"context"
	"errors"
)

// VerifyLinuxEntry returns fresh canonical evidence for the user's explicit
// choice. Persistence belongs to the existing service transaction. No program
// runs here; the caller must retain the environment lock until saving evidence.
func (a Adapter) VerifyLinuxEntry(ctx context.Context, r InstallRequest, resourceID string, selected LinuxProgramCandidate) (LinuxProgramCandidate, error) {
	if err := validateLinuxCandidate(r, selected); err != nil {
		return LinuxProgramCandidate{}, err
	}
	candidates, err := a.LinuxProgramCandidates(ctx, r, resourceID)
	if err != nil {
		return LinuxProgramCandidate{}, err
	}
	for _, candidate := range candidates {
		if candidate.Path == selected.Path && candidate.Kind == selected.Kind {
			if candidate.SHA256 != selected.SHA256 || candidate.CWD != selected.CWD {
				return LinuxProgramCandidate{}, errors.New("selected Linux entry changed; refresh and select it again")
			}
			return candidate, nil
		}
	}
	return LinuxProgramCandidate{}, errors.New("selected Linux entry is no longer installed or discoverable")
}
