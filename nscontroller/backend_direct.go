package nscontroller

// This file implements DirectConsumer, which applies Events directly to a
// Controller's input state with zero serialization overhead:
// no text formatting, no string parsing, no goroutine hops.
//
// It replaces the BackendProxy (text) + nsbackend Coordinator (channel) path
// with a single mutex-guarded struct update + atomic commit.

// DirectConsumer is a Consumer that directly mutates and commits a Controller's
// input state. It is safe to call Consume from multiple goroutines concurrently.
//
// Axis Y values are negated on the way in to match the hardware convention that
// the original nsbackend text-command parser encoded ("ly" → -arg, "ry" → -arg).
type DirectConsumer struct {
	con *Controller
}

// NewDirectConsumer creates a DirectConsumer backed by con.
// con may be in any state (even before Connect() is called): Send() only touches
// the in-memory currentInput snapshot, not the device file.
func NewDirectConsumer(con *Controller) *DirectConsumer {
	return &DirectConsumer{con: con}
}

// Consume applies ev to con immediately.
// It holds con.mu for the entire operation so that the field update and the
// commit (Input → currentInput) are atomic with respect to the ticker that
// reads currentInput in startInputReport.
func (d *DirectConsumer) Consume(ev *Event) {
	con := d.con
	val := ev.Value

	// For digital buttons, any value ≥ 1.0 means "pressed".
	var darg uint8
	if val >= 1.0 {
		darg = 1
	}

	con.mu.Lock()

	switch ev.Action {
	// Face buttons
	case ActionButtonA:
		con.Input.Button.A = darg
	case ActionButtonB:
		con.Input.Button.B = darg
	case ActionButtonX:
		con.Input.Button.X = darg
	case ActionButtonY:
		con.Input.Button.Y = darg

	// Menu buttons
	case ActionButtonMinus:
		con.Input.Button.Minus = darg
	case ActionButtonPlus:
		con.Input.Button.Plus = darg
	case ActionButtonHome:
		con.Input.Button.Home = darg
	case ActionButtonCapture:
		con.Input.Button.Capture = darg

	// Shoulder buttons
	case ActionButtonL:
		con.Input.Button.L = darg
	case ActionButtonR:
		con.Input.Button.R = darg
	case ActionButtonLZ:
		con.Input.Button.ZL = darg
	case ActionButtonRZ:
		con.Input.Button.ZR = darg

	// D-pad
	case ActionButtonDpadUp:
		con.Input.Dpad.Up = darg
	case ActionButtonDpadDown:
		con.Input.Dpad.Down = darg
	case ActionButtonDpadLeft:
		con.Input.Dpad.Left = darg
	case ActionButtonDpadRight:
		con.Input.Dpad.Right = darg

	// Stick presses
	case ActionButtonLeftStickPress:
		con.Input.Stick.Left.Press = darg
	case ActionButtonRightStickPress:
		con.Input.Stick.Right.Press = darg

	// Analog axes — Y is negated: hardware "up" maps to positive Y in ControllerInput.
	case ActionAxisLX:
		con.Input.Stick.Left.X = val
	case ActionAxisLY:
		con.Input.Stick.Left.Y = -val
	case ActionAxisRX:
		con.Input.Stick.Right.X = val
	case ActionAxisRY:
		con.Input.Stick.Right.Y = -val
	}

	// Inline con.Send(): atomically commit Input → currentInput under the same lock,
	// saving a second lock acquisition.
	con.currentInput = con.Input

	con.mu.Unlock()
}

