// v1.4.3: BLE 网关协议层。
//
// 定义端到端可测的二进制帧格式（Flutter flutter_blue_plus 的 notify/write 复用同一格式），
// 旧实现只有内存设备 map、没有任何真实编解码，无法对应真实 GATT 读写。
//
// 帧结构： [0xAA][lenLo:1][lenHi:1][cmd:1][payload:len][crc8:1]
// CRC8(poly=0x07) 覆盖 lenLo..payload 末尾。
package edge

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	SOF        byte = 0xAA
	MaxPayload int  = 512
)

// GATT 命令字。
const (
	CmdBattery byte = 0x01 // payload: 1 byte 电量 0-100
	CmdIntent  byte = 0x02 // payload: UTF-8 转写文本（绝不含音频 PCM）
	CmdAck     byte = 0x03
	CmdReject  byte = 0x04 // payload: 拒绝原因文本
)

var (
	ErrBadSOF     = errors.New("ble: bad start-of-frame")
	ErrBadCRC     = errors.New("ble: crc mismatch")
	ErrPayloadLen = errors.New("ble: payload too long")
	ErrShortFrame = errors.New("ble: frame too short")
)

type Frame struct {
	Cmd     byte
	Payload []byte
}

// Encode 编码为 GATT write 特征值可直接写的字节。
func (f Frame) Encode() ([]byte, error) {
	if len(f.Payload) > MaxPayload {
		return nil, ErrPayloadLen
	}
	out := make([]byte, 0, 3+len(f.Payload)+1)
	out = append(out, SOF, byte(len(f.Payload)), byte(len(f.Payload)>>8), f.Cmd)
	out = append(out, f.Payload...)
	crc := crc8(out[1:]) // 从 lenLo 到 payload
	out = append(out, crc)
	return out, nil
}

// Decode 从 GATT notify 字节流解析一帧，校验 SOF/长度/CRC。
func Decode(raw []byte) (Frame, error) {
	if len(raw) < 4 {
		return Frame{}, ErrShortFrame
	}
	if raw[0] != SOF {
		return Frame{}, ErrBadSOF
	}
	n := int(raw[1]) | int(raw[2])<<8
	if n > MaxPayload {
		return Frame{}, ErrPayloadLen
	}
	total := 4 + n + 1
	if len(raw) < total {
		return Frame{}, ErrShortFrame
	}
	gotCRC := raw[total-1]
	if want := crc8(raw[1 : 4+n]); want != gotCRC {
		return Frame{}, fmt.Errorf("%w: want %02x got %02x", ErrBadCRC, want, gotCRC)
	}
	return Frame{Cmd: raw[3], Payload: raw[4 : 4+n]}, nil
}

// crc8 CRC-8/SMBUS (poly 0x07)。
func crc8(data []byte) byte {
	crc := byte(0x00)
	for _, b := range data {
		crc ^= b
		for i := 0; i < 8; i++ {
			if crc&0x80 != 0 {
				crc = (crc << 1) ^ 0x07
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// ---- 设备会话（端侧 BLE 网关） ----

type DeviceType string

const (
	DeviceEarbud  DeviceType = "earbud"
	DeviceGlasses DeviceType = "glasses"
	DeviceRing    DeviceType = "ring"
)

type Device struct {
	ID        string
	Type      DeviceType
	Battery   int
	Connected bool
}

type BLEGateway struct {
	devices map[string]*Device
}

func NewBLEGateway() *BLEGateway { return &BLEGateway{devices: make(map[string]*Device)} }

func (g *BLEGateway) Connect(id string, t DeviceType) (*Device, error) {
	if _, ok := g.devices[id]; ok {
		return nil, fmt.Errorf("device %s already connected", id)
	}
	d := &Device{ID: id, Type: t, Battery: 80, Connected: true}
	g.devices[id] = d
	return d, nil
}

// OnFrame 处理来自某设备的帧：电量帧更新电量；intent 帧只接受文本。
func (g *BLEGateway) OnFrame(devID string, f Frame) error {
	d, ok := g.devices[devID]
	if !ok {
		return fmt.Errorf("device %s not connected", devID)
	}
	switch f.Cmd {
	case CmdBattery:
		if len(f.Payload) == 1 {
			b := int(f.Payload[0])
			if b > 100 {
				b = 100
			}
			d.Battery = b
		}
	case CmdIntent, CmdAck, CmdReject:
		// 文本帧：仅记录，不把 payload 当音频存储
	}
	return nil
}

func (g *BLEGateway) Disconnect(id string) {
	if d, ok := g.devices[id]; ok {
		d.Connected = false
		delete(g.devices, id)
	}
}

func (g *BLEGateway) List() []*Device {
	out := []*Device{}
	for _, d := range g.devices {
		out = append(out, d)
	}
	return out
}

var _ = binary.LittleEndian
