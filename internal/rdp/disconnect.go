package rdp

import "fmt"

// Disconnect reasons of the control that the code refers to. The full list
// is in the documentation of IMsTscAxEvents::OnDisconnected.
const (
	ReasonNoInfo              = 0
	ReasonLocal               = 1 // we disconnected (not an error)
	ReasonRemoteByUser        = 2 // (not an error)
	ReasonByServer            = 3 // see the extended reason
	ReasonSocketConnectFailed = 516
	ReasonLogonFailure        = 2055
	ReasonSocketClosed        = 2308
	ReasonNoSuchUser          = 2567
)

// Extended disconnect reasons (ExtendedDisconnectReasonCode in the control's
// type library) that the code refers to.
const (
	ExtNoInfo              = 0
	ExtRPCDisconnectByUser = 11
	ExtLogoffByUser        = 12
	ExtInvalidCredentials  = 768
	extProtocolRangeStart  = 4096
	extProtocolRangeEnd    = 32767
)

// reasonText has the texts of IMsTscAxEvents::OnDisconnected, reworded for
// the user.
var reasonText = map[int]string{
	0:    "The session was disconnected.",
	1:    "The session was disconnected.",
	2:    "The session was ended by the user.",
	3:    "The session was ended by the remote computer.",
	260:  "The name of the remote computer could not be found (DNS lookup failed).",
	262:  "Out of memory.",
	264:  "The connection timed out.",
	516:  "Could not connect to the remote computer.",
	518:  "Out of memory.",
	520:  "The remote computer could not be found.",
	772:  "A network error occurred while sending.",
	774:  "Out of memory.",
	776:  "The address of the remote computer is not valid.",
	1028: "A network error occurred while receiving.",
	1030: "The security data is not valid.",
	1032: "An internal error occurred.",
	1286: "The encryption method is not valid.",
	1288: "The name of the remote computer could not be found (DNS lookup failed).",
	1540: "The name of the remote computer could not be found.",
	1542: "The security data of the remote computer is not valid.",
	1544: "An internal timer error occurred.",
	1796: "The connection timed out.",
	1798: "The certificate of the remote computer could not be read.",
	2052: "The address of the remote computer is not valid.",
	2055: "The login failed: wrong user name or password.",
	2056: "The license negotiation failed.",
	2308: "The connection to the remote computer was lost.",
	2310: "An internal security error occurred.",
	2312: "The license negotiation timed out.",
	2566: "An internal security error occurred.",
	2567: "The login failed: the user has no account on the remote computer.",
	2822: "An encryption error occurred.",
	2823: "The account is disabled.",
	3078: "A decryption error occurred.",
	3079: "The account is restricted (it may not log in now, from here, or with an empty password).",
	3080: "A decompression error occurred.",
	3335: "The account is locked out.",
	3591: "The account has expired.",
	3847: "The password has expired.",
	4615: "The password must be changed before logging in for the first time.",
	5639: "The policy does not allow sending the credentials to the remote computer.",
	5895: "Sending the credentials is not allowed because the remote computer could not be authenticated.",
	6151: "No authority could be contacted for authentication.",
	6919: "The certificate of the remote computer has expired or is not valid.",
	7175: "The PIN of the smart card is wrong.",
	8455: "The remote computer does not accept saved credentials. Enter the password again.",
	8711: "The smart card is blocked.",
}

// extendedText has the texts for ExtendedDisconnectReasonCode.
var extendedText = map[int]string{
	1:   "The session was disconnected by an administrator.",
	2:   "The user was logged off by an administrator.",
	3:   "The session was ended because it was idle for too long.",
	4:   "The session was ended because its time limit was reached.",
	5:   "Another connection took over the session.",
	6:   "The remote computer is out of memory.",
	7:   "The remote computer denied the connection.",
	8:   "The remote computer denied the connection for security reasons (FIPS).",
	9:   "The user is not allowed to log in on the remote computer.",
	10:  "The remote computer does not accept saved credentials. Enter the password again.",
	11:  "The session was disconnected by the user.",
	12:  "The user logged off.",
	25:  "The remote computer is shutting down.",
	26:  "The remote computer is restarting.",
	256: "A licensing error occurred on the remote computer.",
	257: "No license server is available.",
	258: "No client access license is available.",
	259: "The remote computer got a licensing message that is not valid.",
	260: "The stored client access license does not match this computer.",
	261: "The stored client access license is not valid.",
	262: "The license negotiation could not be completed.",
	263: "The client ended the license negotiation.",
	264: "The licensing message was not encrypted correctly.",
	265: "The client access license could not be upgraded or renewed.",
	266: "The remote computer is not licensed to accept remote connections.",
	267: "The license store could not be opened: access denied.",
	768: "The login failed: wrong user name or password.",
}

// DisconnectText turns a disconnect reason of the control and its extended
// reason into a readable message. Both are zero when unknown.
func DisconnectText(reason, extended int) string {
	ext, haveExt := extendedText[extended]
	if !haveExt && extended >= extProtocolRangeStart && extended <= extProtocolRangeEnd {
		ext, haveExt = fmt.Sprintf("A protocol error occurred (code %#x).", extended), true
	}
	// The plain reasons say nothing; the extended reason says why.
	if haveExt && reason >= ReasonNoInfo && reason <= ReasonByServer {
		return ext
	}
	if text, ok := reasonText[reason]; ok {
		return text
	}
	if haveExt {
		return ext
	}
	if extended != 0 {
		return fmt.Sprintf("The session was disconnected (reason %d, extended reason %d).", reason, extended)
	}
	return fmt.Sprintf("The session was disconnected (reason %d).", reason)
}

// UserEnded reports whether the session ended on purpose: disconnected from
// this side, or logged off or disconnected by the user in the session. No
// reconnect and no error message is wanted then.
func UserEnded(reason, extended int) bool {
	if reason == ReasonLocal {
		return true
	}
	switch extended {
	case ExtRPCDisconnectByUser, ExtLogoffByUser:
		return true
	case ExtNoInfo:
		return reason == ReasonRemoteByUser
	}
	return false
}

// AuthFailure reports whether the session ended because the server rejected
// the user name or password, so asking the password again makes sense.
func AuthFailure(reason, extended int) bool {
	return reason == ReasonLogonFailure || reason == ReasonNoSuchUser || extended == ExtInvalidCredentials
}
