package sesn

import (
	"time"

	"mynewt.apache.org/newtmgr/nmxact/nmp"
)

type TxOptions struct {
	Timeout time.Duration
	Tries   int
}

func NewTxOptions() TxOptions {
	return TxOptions{
		Timeout: 10 * time.Second,
		Tries:   1,
	}
}

func (opt *TxOptions) AfterTimeout() <-chan time.Time {
	if opt.Timeout == 0 {
		return nil
	} else {
		return time.After(opt.Timeout)
	}
}

// Represents a communication session with a specific peer.  The particulars
// vary according to protocol and transport. Several Sesn instances can use the
// same Xport.
type Sesn interface {
	////// Public interface:

	// Initiates communication with the peer.  For connection-oriented
	// transports, this creates a connection.
	// Returns:
	//     * nil: success.
	//     * nmxutil.SesnAlreadyOpenError: session already open.
	//     * other error
	Open() error

	// Ends communication with the peer.  For connection-oriented transports,
	// this closes the connection.
	//     * nil: success.
	//     * nmxutil.SesnClosedError: session not open.
	//     * other error
	Close() error

	// Indicates whether the session is currently open.
	IsOpen() bool

	// Retrieves the maximum data payload for outgoing NMP requests.
	MtuOut() int

	// Retrieves the maximum data payload for incoming NMP responses.
	MtuIn() int

	// Stops a receive operation in progress.  This must be called from a
	// separate thread, as sesn receive operations are blocking.
	AbortRx(nmpSeq uint8) error

	////// Internal to nmxact:

	EncodeNmpMsg(msg *nmp.NmpMsg) ([]byte, error)

	// Performs a blocking transmit a single NMP message and listens for the
	// response.
	//     * nil: success.
	//     * nmxutil.SesnClosedError: session not open.
	//     * other error
	TxNmpOnce(m *nmp.NmpMsg, opt TxOptions) (nmp.NmpRsp, error)

	GetResourceOnce(uri string, opt TxOptions) ([]byte, error)
	//SetResource(uri string, value []byte, opt TxOptions) error
}
