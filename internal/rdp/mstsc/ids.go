//go:build windows

package mstsc

// Class IDs, interface IDs, vtable slots and event IDs of the control.
//
// Source: the type library of mstscax.dll as dumped by the OLE/COM Object
// Viewer, published as com/mstscax.idl in
// https://github.com/Devolutions/MsRdpEx (MIT). The vtable slots are counted
// through the whole inheritance chain of each interface in that file,
// starting at 0 for IUnknown::QueryInterface.
//
// Only the interfaces that have no IDispatch are called through their
// vtable. Everything scriptable goes through IDispatch::Invoke by name, so
// a control that lacks a property gives an error instead of a wrong call.

// The control classes, newest first.
var classes = []struct {
	name  string
	clsid string
}{
	{"MsRdpClient10NotSafeForScripting", "A0C63C30-F08D-4AB4-907C-34905D770C7D"},
	{"MsRdpClient9NotSafeForScripting", "8B918B82-7985-4C24-89DF-C33AD2BBFBCD"},
}

// The dual interfaces of the control, newest first. Each one inherits the
// ones after it, so its IDispatch knows all their names.
var clientInterfaces = []struct {
	version int
	iid     guid
}{
	{10, mustGUID("7ED92C39-EB38-4927-A70A-708AC5A59321")}, // IMsRdpClient10
	{9, mustGUID("28904001-04B6-436C-A55B-0AF1A0883DC9")},  // IMsRdpClient9
	{8, mustGUID("4247E044-9271-43A9-BC49-E2AD9E855D62")},  // IMsRdpClient8
	{7, mustGUID("B2A5B5CE-3461-444A-91D4-ADD26D070638")},  // IMsRdpClient7
	{6, mustGUID("D43B7D80-8517-4B6D-9EAC-96AD6800D7F2")},  // IMsRdpClient6
	{5, mustGUID("4EB5335B-6429-477D-B922-E06A28ECD8BF")},  // IMsRdpClient5
	{4, mustGUID("095E0738-D97D-488B-B9F6-DD0E8D66C0DE")},  // IMsRdpClient4
	{3, mustGUID("91B7CBC5-A72E-4FA0-9300-D647D7E897FF")},  // IMsRdpClient3
	{2, mustGUID("E7E17DC4-3B71-4BA7-A8E6-281FFADCA28F")},  // IMsRdpClient2
	{1, mustGUID("92B4A539-7115-4B7C-A5A9-E5D9EFC2780A")},  // IMsRdpClient
}

var (
	iidIUnknown                  = mustGUID("00000000-0000-0000-C000-000000000046")
	iidIDispatch                 = mustGUID("00020400-0000-0000-C000-000000000046")
	iidIConnectionPointContainer = mustGUID("B196B284-BAB4-101A-B69C-00AA00341D07")

	diidIMsTscAxEvents = mustGUID("336D5562-EFA8-482E-8CB3-C5C0FC7A7DB6")

	iidIMsTscNonScriptable        = mustGUID("C1E6743A-41C1-4A74-832A-0DD06C1C7A0E")
	iidIMsRdpClientNonScriptable  = mustGUID("2F079C4C-87B2-4AFD-97AB-20CDB43038AE")
	iidIMsRdpClientNonScriptable3 = mustGUID("B3378D90-0728-45C7-8ED7-B6159FB92219")
	iidIMsRdpClientNonScriptable5 = mustGUID("4F6996D5-D7B1-412C-B0FF-063718566907")
	iidIMsRdpExtendedSettings     = mustGUID("302D8188-0052-4807-806A-362B628F9AC5")
)

// Vtable slots. IUnknown has 0 to 2.
const (
	// IMsTscNonScriptable : IUnknown
	slotPutClearTextPassword = 3  // (BSTR)
	slotResetPassword        = 12 // ()

	// IMsRdpClientNonScriptable : IMsTscNonScriptable (slots 13, 14)
	slotSendKeys = 14 // (long numKeys, VARIANT_BOOL* keyUp, long* keyData)

	// IMsRdpClientNonScriptable3 : ...NonScriptable2 (15, 16) : ...NonScriptable
	slotGetDriveCollection = 30 // (IMsRdpDriveCollection**)

	// IMsRdpClientNonScriptable5 : ...NonScriptable4 (37 to 52) : ...NonScriptable3 (17 to 36)
	slotPutAllowPromptingForCredentials = 63 // (VARIANT_BOOL)

	// IMsRdpExtendedSettings : IUnknown
	slotPutProperty = 3 // (BSTR name, VARIANT* value)

	// IMsRdpDriveCollection : IUnknown
	slotGetDriveByIndex = 4 // (unsigned long index, IMsRdpDrive**)
	slotGetDriveCount   = 5 // (unsigned long*)

	// IMsRdpDrive : IUnknown
	slotDriveGetName             = 3 // (BSTR*)
	slotDrivePutRedirectionState = 4 // (VARIANT_BOOL)

	// IConnectionPointContainer and IConnectionPoint (ocidl.h)
	slotFindConnectionPoint = 4 // (REFIID, IConnectionPoint**)
	slotAdvise              = 5 // (IUnknown* sink, DWORD* cookie)
	slotUnadvise            = 6 // (DWORD cookie)

	// IDispatch (oaidl.h)
	slotGetIDsOfNames = 5
	slotInvoke        = 6
)

// DISPIDs of the dispinterface IMsTscAxEvents.
const (
	evConnecting                     = 1
	evConnected                      = 2
	evLoginComplete                  = 3
	evDisconnected                   = 4  // (long reason)
	evEnterFullScreenMode            = 5  // the control's own full screen, not used
	evLeaveFullScreenMode            = 6  //
	evRequestGoFullScreen            = 8  // Ctrl+Alt+Break
	evRequestLeaveFullScreen         = 9  //
	evFatalError                     = 10 // (long code)
	evWarning                        = 11 // (long code)
	evRemoteDesktopSizeChange        = 12 // (long width, long height)
	evIdleTimeoutNotification        = 13
	evRequestContainerMinimize       = 14
	evConfirmClose                   = 15 // returns VARIANT_BOOL allow
	evReceivedTSPublicKey            = 16 // (BSTR key) returns VARIANT_BOOL continue
	evAutoReconnecting               = 17 // (long reason, long attempt) returns AutoReconnectContinueState
	evAuthenticationWarningDisplayed = 18
	evAuthenticationWarningDismissed = 19
	evLogonError                     = 22 // (long error)
	evFocusReleased                  = 23 // (int direction)
	evNetworkStatusChanged           = 32 // (ulong quality, long bandwidth, long rtt)
	evAutoReconnected                = 33
	evAutoReconnecting2              = 34 // (long reason, VARIANT_BOOL networkAvailable, long attempt, long maxAttempts)
)

// RemoteSessionActionType for SendRemoteAction.
const actionStartScreen = 3

// Values of the control's settings.
const (
	networkLAN = 6 // NetworkConnectionType: 10 Mbps or more

	keyboardHookLocal      = 0 // KeyboardHookMode
	keyboardHookRemote     = 1
	keyboardHookFullScreen = 2

	audioLocal  = 0 // AudioRedirectionMode
	audioRemote = 1
	audioNone   = 2

	authNone = 0 // AuthenticationLevel: connect without checking the server
	authWarn = 2 // ask the user when the server cannot be verified
)
