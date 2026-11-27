package metis

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// ErrSecondFactorRequired is what [Client.Login] answers, wrapped in a
// [*SecondFactorError], when the password was right and the server asks for a
// code from an authenticator app before it gives a session.
var ErrSecondFactorRequired = errors.New("metis: the server asks for a code from an authenticator app")

// The steps a [SecondFactorError] names.
const (
	// SecondFactorCode asks for the code the account's authenticator app shows
	// now, or one of its recovery codes: see [Client.LoginWithCode].
	SecondFactorCode = "code"
	// SecondFactorEnrol means the account has no authenticator app yet. It
	// sets one up by signing in to the web app once.
	SecondFactorEnrol = "enrol"
)

// SecondFactorError is a correct password the server wants a second factor
// for. It matches [ErrSecondFactorRequired] with errors.Is.
type SecondFactorError struct {
	// Step is [SecondFactorCode] or [SecondFactorEnrol].
	Step string
	// Challenge is good for this step alone, until ExpiresAt. It is not a
	// session and opens nothing else.
	Challenge string
	// ExpiresAt is when Challenge stops working; sign in again after it.
	ExpiresAt time.Time
}

func (e *SecondFactorError) Error() string {
	if e.Step == SecondFactorEnrol {
		return "metis: this account must set up an authenticator app first; sign in to the web app once, or use an API key"
	}
	return "metis: the server asks for a code from an authenticator app; call LoginWithCode, or use an API key"
}

// Is makes errors.Is(err, ErrSecondFactorRequired) true.
func (e *SecondFactorError) Is(target error) bool {
	return target == ErrSecondFactorRequired
}

// LoginWithCode finishes a sign-in [Client.Login] answered with a
// [*SecondFactorError] of step [SecondFactorCode]: challenge is that error's
// Challenge, and code the six digits the authenticator app shows, or one of the
// account's recovery codes. Each code works once. The session is stored on the
// client, as after a Login that needed no code.
func (c *Client) LoginWithCode(ctx context.Context, challenge, code string) error {
	var out signInAnswer
	err := c.do(ctx, http.MethodPost, "/api/v1/login/code", map[string]string{
		"challenge": challenge,
		"code":      code,
	}, &out)
	if err != nil {
		return err
	}
	return c.keepSession(out)
}

// signInAnswer is what /login and /login/code answer: a session, or the step
// still to take.
type signInAnswer struct {
	Token              string    `json:"token"`
	User               *User     `json:"user"`
	SecondFactor       string    `json:"second_factor"`
	Challenge          string    `json:"challenge"`
	ChallengeExpiresAt time.Time `json:"challenge_expires_at"`
}

// keepSession stores the session a sign-in answered with, or turns the step
// still to take into a [*SecondFactorError].
func (c *Client) keepSession(out signInAnswer) error {
	if out.SecondFactor != "" {
		return &SecondFactorError{
			Step:      out.SecondFactor,
			Challenge: out.Challenge,
			ExpiresAt: out.ChallengeExpiresAt,
		}
	}
	if out.Token == "" {
		return fmt.Errorf("metis: sign-in answered without a token")
	}

	c.mu.Lock()
	c.token = out.Token
	c.person = false
	c.me = nil
	if out.User != nil && out.User.Username != "" {
		c.me = out.User
	}
	c.mu.Unlock()
	return nil
}
