package upgrade

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

var (
	JournalMagic  = []byte("HAUPJNL1")
	SnapshotMagic = []byte("HAUPSNP1")
	crc32cTable   = crc32.MakeTable(crc32.Castagnoli)
)

const (
	MaxRecordBytes = 1024 * 1024 // 1 MiB per record
)

// TLVType 标识 TLV 字段数据类型。
type TLVType uint8

const (
	TLVTypeU64   TLVType = 1
	TLVTypeBytes TLVType = 2
	TLVTypeUTF8  TLVType = 3
	TLVTypeBool  TLVType = 4
)

// TLVField 描述单个确定性 TLV 属性字段。
type TLVField struct {
	FieldID uint16
	Type    TLVType
	Value   []byte
}

// EncodeTLVFields 将 TLV 字段按 FieldID 严格升序编码，禁止重复字段。
func EncodeTLVFields(fields []TLVField) ([]byte, error) {
	// 按 FieldID 升序排序
	sort.Slice(fields, func(i, j int) bool {
		return fields[i].FieldID < fields[j].FieldID
	})

	buf := new(bytes.Buffer)
	var prevID uint16
	for idx, f := range fields {
		if idx > 0 && f.FieldID <= prevID {
			return nil, fmt.Errorf("duplicate or out-of-order field_id %d", f.FieldID)
		}
		prevID = f.FieldID

		// 2 bytes FieldID
		idBuf := make([]byte, 2)
		binary.BigEndian.PutUint16(idBuf, f.FieldID)
		buf.Write(idBuf)

		// 1 byte Type
		buf.WriteByte(byte(f.Type))

		// 4 bytes Length
		lenBuf := make([]byte, 4)
		binary.BigEndian.PutUint32(lenBuf, uint32(len(f.Value)))
		buf.Write(lenBuf)

		// Value
		buf.Write(f.Value)
	}

	return buf.Bytes(), nil
}

// DecodeTLVFields 从字节流中严格解析 TLV 字段列表。
func DecodeTLVFields(data []byte) ([]TLVField, error) {
	var fields []TLVField
	reader := bytes.NewReader(data)
	var prevID uint16
	first := true

	for reader.Len() > 0 {
		var id uint16
		if err := binary.Read(reader, binary.BigEndian, &id); err != nil {
			return nil, fmt.Errorf("read field_id: %w", err)
		}

		if !first && id <= prevID {
			return nil, fmt.Errorf("duplicate or out-of-order field_id %d", id)
		}
		first = false
		prevID = id

		typeByte, err := reader.ReadByte()
		if err != nil {
			return nil, fmt.Errorf("read field type: %w", err)
		}
		t := TLVType(typeByte)
		if t < TLVTypeU64 || t > TLVTypeBool {
			return nil, fmt.Errorf("unknown TLV type %d for field_id %d", t, id)
		}

		var length uint32
		if err := binary.Read(reader, binary.BigEndian, &length); err != nil {
			return nil, fmt.Errorf("read field length: %w", err)
		}

		val := make([]byte, length)
		if _, err := io.ReadFull(reader, val); err != nil {
			return nil, fmt.Errorf("read field value (len=%d): %w", length, err)
		}

		fields = append(fields, TLVField{
			FieldID: id,
			Type:    t,
			Value:   val,
		})
	}

	return fields, nil
}

// JournalRecord 表示单条落盘记录结构。
type JournalRecord struct {
	Generation       uint64
	JournalRevision  uint64
	PreviousHash     [32]byte
	Tag              RecordTag
	Payload          []byte
	RecordHash       [32]byte
}

// JournalState 维护回放后的内存状态快照。
type JournalState struct {
	Generation          uint64
	LastJournalRevision uint64
	LastRecordHash      [32]byte
	CurrentState        State
	CommandID           string
	TransactionID       string
	SecurityMode        SecurityMode
	ReleaseSequence     uint64
	ManifestDigest      string
	RunningBundleDigest string
	FenceRevision       uint64
	FenceDigest         string
	SeenReleases        map[uint64]string // release_sequence -> manifest_digest
	PendingEvents       []TLVField
	RawFields           map[uint16]TLVField
}

// Journal 管理 HAUPJNL1 事务文件的并发追加、回放与压缩。
type Journal struct {
	mu          sync.Mutex
	dir         string
	journalPath string
	state       *JournalState
}

// OpenJournal 打开或初始化指定目录下的事务日志管理器。
func OpenJournal(dir string) (*Journal, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("mkdir journal dir: %w", err)
	}

	j := &Journal{
		dir:         dir,
		journalPath: filepath.Join(dir, "transaction.journal"),
		state: &JournalState{
			Generation:   1,
			SeenReleases: make(map[uint64]string),
			RawFields:    make(map[uint16]TLVField),
		},
	}

	if err := j.playback(); err != nil {
		return nil, fmt.Errorf("playback journal: %w", err)
	}

	return j, nil
}

func (j *Journal) playback() error {
	f, err := os.OpenFile(j.journalPath, os.O_RDONLY, 0600)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	// 检查 Header Magic
	magic := make([]byte, len(JournalMagic))
	if _, err := io.ReadFull(f, magic); err != nil {
		if err == io.EOF || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil
		}
		return fmt.Errorf("read magic: %w", err)
	}
	if !bytes.Equal(magic, JournalMagic) {
		return fmt.Errorf("invalid journal magic: %s", string(magic))
	}

	var prevHash [32]byte
	var lastRev uint64

	for {
		var recordLen uint32
		err := binary.Read(f, binary.BigEndian, &recordLen)
		if err != nil {
			if err == io.EOF || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return fmt.Errorf("read record length: %w", err)
		}
		if recordLen > MaxRecordBytes {
			return fmt.Errorf("record length %d exceeds max %d", recordLen, MaxRecordBytes)
		}

		body := make([]byte, recordLen)
		if _, err := io.ReadFull(f, body); err != nil {
			// 文件末尾单条未完成截断，允许忽略末尾半写
			break
		}

		// 计算并验证 CRC32C（最后 4 字节为 CRC）
		if len(body) < 4 {
			break
		}
		crcExpected := binary.BigEndian.Uint32(body[len(body)-4:])
		crcData := body[:len(body)-4]
		crcActual := crc32.Checksum(crcData, crc32cTable)
		if crcActual != crcExpected {
			// 校验不通过，停止回放
			break
		}

		// 解析 Record 内容: generation(8) + revision(8) + prevHash(32) + tag(2) + payloadLen(4) + payload
		reader := bytes.NewReader(crcData)
		var gen, rev uint64
		var readPrevHash [32]byte
		var tag uint16
		var payloadLen uint32

		_ = binary.Read(reader, binary.BigEndian, &gen)
		_ = binary.Read(reader, binary.BigEndian, &rev)
		_, _ = io.ReadFull(reader, readPrevHash[:])
		_ = binary.Read(reader, binary.BigEndian, &tag)
		_ = binary.Read(reader, binary.BigEndian, &payloadLen)

		payload := make([]byte, payloadLen)
		_, _ = io.ReadFull(reader, payload)

		// 验证哈希链
		if lastRev > 0 && readPrevHash != prevHash {
			return fmt.Errorf("journal hash chain broken at revision %d", rev)
		}

		// 计算当前记录 Hash（从 record_length 到 CRC 整体）
		recordFull := new(bytes.Buffer)
		_ = binary.Write(recordFull, binary.BigEndian, recordLen)
		recordFull.Write(body)
		prevHash = sha256.Sum256(recordFull.Bytes())
		lastRev = rev

		// 解析 TLV 并应用到状态
		fields, err := DecodeTLVFields(payload)
		if err == nil {
			j.applyFieldsToState(RecordTag(tag), gen, rev, fields)
		}
	}

	j.state.LastJournalRevision = lastRev
	j.state.LastRecordHash = prevHash
	return nil
}

func (j *Journal) applyFieldsToState(tag RecordTag, gen, rev uint64, fields []TLVField) {
	j.state.Generation = gen
	j.state.LastJournalRevision = rev

	for _, f := range fields {
		j.state.RawFields[f.FieldID] = f
		switch f.FieldID {
		case 1: // command_id
			j.state.CommandID = string(f.Value)
		case 2: // transaction_id
			j.state.TransactionID = string(f.Value)
		case 3: // state
			if len(f.Value) == 8 {
				j.state.CurrentState = State(binary.BigEndian.Uint64(f.Value))
			}
		case 19: // release_sequence
			if len(f.Value) == 8 {
				j.state.ReleaseSequence = binary.BigEndian.Uint64(f.Value)
			}
		case 20: // manifest_digest
			j.state.ManifestDigest = string(f.Value)
		case 21: // bundle_digest
			j.state.RunningBundleDigest = string(f.Value)
		case 23: // fence_revision
			if len(f.Value) == 8 {
				j.state.FenceRevision = binary.BigEndian.Uint64(f.Value)
			}
		case 25: // fence_digest
			j.state.FenceDigest = string(f.Value)
		case 31: // security_mode
			if len(f.Value) == 8 {
				j.state.SecurityMode = SecurityMode(binary.BigEndian.Uint64(f.Value))
			}
		}
	}
}

// AppendRecord 原子写入单条 TLV 日志记录并同步落盘（fsync）。
func (j *Journal) AppendRecord(tag RecordTag, fields []TLVField) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	payload, err := EncodeTLVFields(fields)
	if err != nil {
		return fmt.Errorf("encode TLV fields: %w", err)
	}

	rev := j.state.LastJournalRevision + 1
	gen := j.state.Generation
	if gen == 0 {
		gen = 1
	}

	// 构造待写入 Record Payload
	bodyBuf := new(bytes.Buffer)
	_ = binary.Write(bodyBuf, binary.BigEndian, gen)
	_ = binary.Write(bodyBuf, binary.BigEndian, rev)
	bodyBuf.Write(j.state.LastRecordHash[:])
	_ = binary.Write(bodyBuf, binary.BigEndian, uint16(tag))
	_ = binary.Write(bodyBuf, binary.BigEndian, uint32(len(payload)))
	bodyBuf.Write(payload)

	// 计算 CRC32C
	crc := crc32.Checksum(bodyBuf.Bytes(), crc32cTable)
	_ = binary.Write(bodyBuf, binary.BigEndian, crc)

	recordLen := uint32(bodyBuf.Len())
	recordBytes := new(bytes.Buffer)
	_ = binary.Write(recordBytes, binary.BigEndian, recordLen)
	recordBytes.Write(bodyBuf.Bytes())

	// 打开文件写入
	isNew := false
	if _, err := os.Stat(j.journalPath); os.IsNotExist(err) {
		isNew = true
	}

	f, err := os.OpenFile(j.journalPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return fmt.Errorf("open journal file: %w", err)
	}
	defer f.Close()

	if isNew {
		if _, err := f.Write(JournalMagic); err != nil {
			return fmt.Errorf("write journal magic: %w", err)
		}
	}

	if _, err := f.Write(recordBytes.Bytes()); err != nil {
		return fmt.Errorf("write record: %w", err)
	}

	if err := f.Sync(); err != nil {
		return fmt.Errorf("fsync journal: %w", err)
	}

	// 更新状态
	j.state.LastJournalRevision = rev
	j.state.LastRecordHash = sha256.Sum256(recordBytes.Bytes())
	j.applyFieldsToState(tag, gen, rev, fields)

	return nil
}

// GetState 获取当前回放后的内存状态副本。
func (j *Journal) GetState() JournalState {
	j.mu.Lock()
	defer j.mu.Unlock()
	return *j.state
}
