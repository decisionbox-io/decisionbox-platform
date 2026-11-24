package handler

import (
	"errors"
	"regexp"
)

// credentialInURI matches the userinfo of a URI — "scheme://user:secret@host" —
// capturing everything except the secret. Warehouse drivers quote the connection
// string they failed to parse back in their error message, so this is the shape
// a credential arrives in.
//
// Two deliberate choices, both because the string we are handed is one a driver
// REFUSED to parse — so it is exactly where the well-formedness assumptions a
// tighter pattern would make are least safe:
//
//   - The user group stops at the first ":" rather than excluding "@", so an
//     email-style username ("user@corp.com:secret@host", which Azure and
//     Snowflake DSNs use) still matches instead of falling through unredacted.
//   - The password group is greedy to the LAST "@" before whitespace, so a
//     generated password containing "@" or "/" is covered whole. A tighter class
//     stopped at the first "@" and left the remainder of the password in the
//     message — caught by the punctuation case in TestRedactCredentials.
//
// The cost of greediness is over-redaction in a contrived message, which is the
// right way to be wrong about a password.
var credentialInURI = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^:/\s]*):[^\s]*@`)

// redactCredentials returns err with any URI password replaced by "***".
//
// For errors that reach a log sink or a response: a sink is read by more people
// than the deployment's admins, and a warehouse connection string carries the
// customer's database password. The rest of the message — scheme, user, host,
// and the driver's own reason — survives, because a redacted diagnostic is still
// a diagnostic and a dropped one leaves nobody able to say why a listing failed.
//
// A nil error passes through, so callers don't need to guard.
func redactCredentials(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	clean := credentialInURI.ReplaceAllString(msg, "$1:***@")
	if clean == msg {
		return err
	}
	return errors.New(clean)
}
