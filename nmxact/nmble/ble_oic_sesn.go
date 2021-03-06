package nmble

import (
	"fmt"
	"sync"
	"time"

	"github.com/runtimeco/go-coap"

	"mynewt.apache.org/newt/util"
	. "mynewt.apache.org/newtmgr/nmxact/bledefs"
	"mynewt.apache.org/newtmgr/nmxact/nmp"
	"mynewt.apache.org/newtmgr/nmxact/nmxutil"
	"mynewt.apache.org/newtmgr/nmxact/oic"
	"mynewt.apache.org/newtmgr/nmxact/omp"
	"mynewt.apache.org/newtmgr/nmxact/sesn"
)

type BleOicSesn struct {
	bf           *BleFsm
	rxer         *omp.Receiver
	closeTimeout time.Duration
	onCloseCb    sesn.OnCloseFn

	closeChan chan error
	mtx       sync.Mutex
}

func NewBleOicSesn(bx *BleXport, cfg sesn.SesnCfg) *BleOicSesn {
	bos := &BleOicSesn{
		rxer:         omp.NewReceiver(true),
		closeTimeout: cfg.Ble.CloseTimeout,
		onCloseCb:    cfg.OnCloseCb,
	}

	iotUuid, err := ParseUuid(IotivitySvcUuid)
	svcUuids := []BleUuid{
		{U16: OmpSvcUuid},
		iotUuid,
	}

	reqChrUuid, err := ParseUuid(OmpReqChrUuid)
	if err != nil {
		panic(err.Error())
	}

	rspChrUuid, err := ParseUuid(OmpRspChrUuid)
	if err != nil {
		panic(err.Error())
	}

	bos.bf = NewBleFsm(BleFsmParams{
		Bx:          bx,
		OwnAddrType: cfg.Ble.OwnAddrType,
		Central: BleFsmParamsCentral{
			PeerDev:   cfg.PeerSpec.Ble,
			ConnTries: cfg.Ble.Central.ConnTries,
		},
		SvcUuids:    svcUuids,
		ReqChrUuid:  reqChrUuid,
		RspChrUuid:  rspChrUuid,
		EncryptWhen: cfg.Ble.EncryptWhen,
		RxDataCb:    func(d []byte) { bos.onRxNmp(d) },
		DisconnectCb: func(dt BleFsmDisconnectType, p BleDev, e error) {
			bos.onDisconnect(dt, p, e)
		},
	})

	return bos
}

// Returns true if a new channel was assigned.
func (bos *BleOicSesn) setCloseChan() error {
	bos.mtx.Lock()
	defer bos.mtx.Unlock()

	if bos.closeChan != nil {
		return fmt.Errorf("Multiple listeners waiting for session to close")
	}

	bos.closeChan = make(chan error, 1)
	return nil
}

func (bos *BleOicSesn) clearCloseChan() {
	bos.mtx.Lock()
	defer bos.mtx.Unlock()

	bos.closeChan = nil
}

func (bos *BleOicSesn) listenForClose(timeout time.Duration) error {
	select {
	case <-bos.closeChan:
		return nil
	case <-time.After(timeout):
		// Session never closed.
		return fmt.Errorf("Timeout while waiting for session to close")
	}
}

func (bos *BleOicSesn) blockUntilClosed(timeout time.Duration) error {
	if err := bos.setCloseChan(); err != nil {
		return err
	}
	defer bos.clearCloseChan()

	// If the session is already closed, we're done.
	if bos.bf.IsClosed() {
		return nil
	}

	// Block until close completes or times out.
	return bos.listenForClose(timeout)
}

func (bos *BleOicSesn) AbortRx(seq uint8) error {
	return bos.rxer.FakeNmpError(seq, fmt.Errorf("Rx aborted"))
}

func (bos *BleOicSesn) Open() error {
	return bos.bf.Start()
}

func (bos *BleOicSesn) Close() error {
	if err := bos.setCloseChan(); err != nil {
		return err
	}
	defer bos.clearCloseChan()

	done, err := bos.bf.Stop()
	if err != nil {
		return err
	}

	if done {
		// Close complete.
		return nil
	}

	// Block until close completes or times out.
	return bos.listenForClose(bos.closeTimeout)
}

func (bos *BleOicSesn) IsOpen() bool {
	return bos.bf.IsOpen()
}

func (bos *BleOicSesn) onRxNmp(data []byte) {
	bos.rxer.Rx(data)
}

// Called by the FSM when a blehostd disconnect event is received.
func (bos *BleOicSesn) onDisconnect(dt BleFsmDisconnectType, peer BleDev,
	err error) {

	bos.rxer.ErrorAll(err)

	bos.mtx.Lock()

	// If the session is being closed, unblock the close() call.
	if bos.closeChan != nil {
		bos.closeChan <- err
	}

	bos.mtx.Unlock()

	// Only execute client's disconnect callback if the disconnect was
	// unsolicited and the session was fully open.
	if dt == FSM_DISCONNECT_TYPE_OPENED && bos.onCloseCb != nil {
		bos.onCloseCb(bos, err)
	}
}

func (bos *BleOicSesn) EncodeNmpMsg(m *nmp.NmpMsg) ([]byte, error) {
	return omp.EncodeOmpTcp(m)
}

// Blocking.
func (bos *BleOicSesn) TxNmpOnce(m *nmp.NmpMsg, opt sesn.TxOptions) (
	nmp.NmpRsp, error) {

	if !bos.IsOpen() {
		return nil, bos.bf.closedError(
			"Attempt to transmit over closed BLE session")
	}

	nl, err := bos.rxer.AddNmpListener(m.Hdr.Seq)
	if err != nil {
		return nil, err
	}
	defer bos.rxer.RemoveNmpListener(m.Hdr.Seq)

	b, err := bos.EncodeNmpMsg(m)
	if err != nil {
		return nil, err
	}

	return bos.bf.TxNmp(b, nl, opt.Timeout)
}

func (bos *BleOicSesn) MtuIn() int {
	return bos.bf.attMtu -
		NOTIFY_CMD_BASE_SZ -
		omp.OMP_MSG_OVERHEAD -
		nmp.NMP_HDR_SIZE
}

func (bos *BleOicSesn) MtuOut() int {
	mtu := bos.bf.attMtu -
		WRITE_CMD_BASE_SZ -
		omp.OMP_MSG_OVERHEAD -
		nmp.NMP_HDR_SIZE
	return util.IntMin(mtu, BLE_ATT_ATTR_MAX_LEN)
}

func (bos *BleOicSesn) ConnInfo() (BleConnDesc, error) {
	return bos.bf.connInfo()
}

func (bos *BleOicSesn) GetResourceOnce(uri string, opt sesn.TxOptions) (
	[]byte, error) {

	token := nmxutil.NextOicToken()

	ol, err := bos.rxer.AddOicListener(token)
	if err != nil {
		return nil, err
	}
	defer bos.rxer.RemoveOicListener(token)

	req, err := oic.EncodeGet(uri, token)
	if err != nil {
		return nil, err
	}

	rsp, err := bos.bf.TxOic(req, ol, opt.Timeout)
	if err != nil {
		return nil, err
	}

	if rsp.Code != coap.Content {
		return nil, fmt.Errorf("UNEXPECTED OIC ACK: %#v", rsp)
	}

	return rsp.Payload, nil
}
