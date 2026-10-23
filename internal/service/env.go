package service

import (
	"errors"
	"fmt"
	"os"

	"github.com/XSpring/minipsi/internal/orcapi"
)

// Environment variables that tell this CC who it is and where the orchestrator is. A .env file
// can hold them; minipsi does not read the file itself, so load it into the environment, for
// example with `set -a; . ./.env; set +a`, `docker run --env-file .env` or systemd's
// EnvironmentFile=.
const (
	// EnvCCID is the ID of this CC, as the ORC knows it.
	EnvCCID = "MINIPSI_CC_ID"
	// EnvORCURL is the base URL of the ORC, http or https.
	EnvORCURL = "MINIPSI_ORC_URL"
)

// ORCFromEnv returns this CC's ID and the ORC's base URL from EnvCCID and EnvORCURL. It
// reports every missing or invalid variable at once; its errors do not repeat the values.
func ORCFromEnv() (ccID, orcURL string, err error) {
	var errs []error
	ccID = os.Getenv(EnvCCID)
	switch {
	case ccID == "":
		errs = append(errs, fmt.Errorf("%s is not set", EnvCCID))
	case !orcapi.ValidCCID(ccID):
		errs = append(errs, fmt.Errorf("%s: invalid CC ID", EnvCCID))
	}
	orcURL = os.Getenv(EnvORCURL)
	if orcURL == "" {
		errs = append(errs, fmt.Errorf("%s is not set", EnvORCURL))
	} else if _, err := parseORCURL(orcURL); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", EnvORCURL, err))
	}
	if err := errors.Join(errs...); err != nil {
		return "", "", err
	}
	return ccID, orcURL, nil
}
