package orchard

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"unicode/utf16"
)

const maxBinaryPlistObjects = 100_000

type binaryPlist struct {
	data      []byte
	offsets   []uint64
	refSize   int
	objectEnd uint64
}

// parseBinaryPlistStrings decodes the bounded scalar fields used by Apple
// version metadata. It deliberately does not expose arbitrary object graphs.
func parseBinaryPlistStrings(data []byte) (map[string]string, error) {
	if len(data) < 40 || string(data[:8]) != "bplist00" {
		return nil, errors.New("unsupported binary plist header")
	}
	trailer := data[len(data)-32:]
	offsetSize, refSize := int(trailer[6]), int(trailer[7])
	objects := binary.BigEndian.Uint64(trailer[8:16])
	top := binary.BigEndian.Uint64(trailer[16:24])
	offsetTable := binary.BigEndian.Uint64(trailer[24:32])
	if offsetSize < 1 || offsetSize > 8 || refSize < 1 || refSize > 8 || objects == 0 || objects > maxBinaryPlistObjects || top >= objects {
		return nil, errors.New("binary plist trailer is invalid")
	}
	tableBytes := objects * uint64(offsetSize)
	if offsetTable < 8 || offsetTable > uint64(len(data)-32) || tableBytes > uint64(len(data)-32)-offsetTable {
		return nil, errors.New("binary plist offset table is out of bounds")
	}
	parser := binaryPlist{data: data, offsets: make([]uint64, int(objects)), refSize: refSize, objectEnd: offsetTable}
	for index := uint64(0); index < objects; index++ {
		start := offsetTable + index*uint64(offsetSize)
		offset, err := readSizedUint(data[start:start+uint64(offsetSize)], offsetSize)
		if err != nil || offset < 8 || offset >= offsetTable {
			return nil, errors.New("binary plist object offset is invalid")
		}
		parser.offsets[int(index)] = offset
	}
	return parser.readTopDictionary(top)
}

func (p binaryPlist) readTopDictionary(reference uint64) (map[string]string, error) {
	marker, info, offset, err := p.marker(reference)
	if err != nil {
		return nil, err
	}
	if marker != 0xd {
		return nil, errors.New("binary plist top object is not a dictionary")
	}
	count, cursor, err := p.objectLength(info, offset+1)
	if err != nil {
		return nil, err
	}
	if count > maxBinaryPlistObjects {
		return nil, errors.New("binary plist dictionary is too large")
	}
	referenceBytes := count * uint64(p.refSize) * 2
	if cursor > p.objectEnd || referenceBytes > p.objectEnd-cursor {
		return nil, errors.New("binary plist dictionary references are out of bounds")
	}
	values := make(map[string]string, int(count))
	seen := make(map[string]bool, int(count))
	for index := uint64(0); index < count; index++ {
		keyReference, err := readSizedUint(p.data[cursor+index*uint64(p.refSize):], p.refSize)
		if err != nil {
			return nil, err
		}
		valueBase := cursor + count*uint64(p.refSize)
		valueReference, err := readSizedUint(p.data[valueBase+index*uint64(p.refSize):], p.refSize)
		if err != nil {
			return nil, err
		}
		key, ok, err := p.scalar(keyReference)
		if err != nil || !ok || key == "" {
			return nil, errors.New("binary plist dictionary key is not a string")
		}
		if seen[key] {
			return nil, errors.New("binary plist dictionary contains a duplicate key")
		}
		seen[key] = true
		if value, scalar, err := p.scalar(valueReference); err != nil {
			return nil, err
		} else if scalar {
			values[key] = value
		}
	}
	if len(values) == 0 {
		return nil, errors.New("binary plist contained no supported scalar metadata")
	}
	return values, nil
}

func (p binaryPlist) scalar(reference uint64) (string, bool, error) {
	marker, info, offset, err := p.marker(reference)
	if err != nil {
		return "", false, err
	}
	switch marker {
	case 0x1:
		if info > 3 {
			return "", false, errors.New("binary plist integer is too wide")
		}
		size := 1 << info
		if offset+1+uint64(size) > p.objectEnd {
			return "", false, errors.New("binary plist integer is truncated")
		}
		value, err := readSizedUint(p.data[offset+1:], size)
		if err != nil {
			return "", false, err
		}
		return strconv.FormatUint(value, 10), true, nil
	case 0x5, 0x6:
		count, cursor, err := p.objectLength(info, offset+1)
		if err != nil {
			return "", false, err
		}
		if marker == 0x5 {
			if cursor > p.objectEnd || count > p.objectEnd-cursor {
				return "", false, errors.New("binary plist ASCII string is truncated")
			}
			bytes := p.data[cursor : cursor+count]
			for _, value := range bytes {
				if value > 0x7f {
					return "", false, errors.New("binary plist ASCII string contains a non-ASCII byte")
				}
			}
			return string(bytes), true, nil
		}
		if count > (p.objectEnd-cursor)/2 {
			return "", false, errors.New("binary plist UTF-16 string is truncated")
		}
		units := make([]uint16, int(count))
		for index := range units {
			units[index] = binary.BigEndian.Uint16(p.data[cursor+uint64(index*2):])
		}
		return string(utf16.Decode(units)), true, nil
	default:
		return "", false, nil
	}
}

func (p binaryPlist) marker(reference uint64) (byte, byte, uint64, error) {
	if reference >= uint64(len(p.offsets)) {
		return 0, 0, 0, errors.New("binary plist object reference is invalid")
	}
	offset := p.offsets[int(reference)]
	if offset >= p.objectEnd {
		return 0, 0, 0, errors.New("binary plist object is out of bounds")
	}
	value := p.data[offset]
	return value >> 4, value & 0x0f, offset, nil
}

func (p binaryPlist) objectLength(info byte, cursor uint64) (uint64, uint64, error) {
	if info < 0x0f {
		return uint64(info), cursor, nil
	}
	if cursor >= p.objectEnd || p.data[cursor]>>4 != 0x1 {
		return 0, 0, errors.New("binary plist extended length is invalid")
	}
	power := p.data[cursor] & 0x0f
	if power > 3 {
		return 0, 0, errors.New("binary plist extended length is too wide")
	}
	size := 1 << power
	if cursor+1+uint64(size) > p.objectEnd {
		return 0, 0, errors.New("binary plist extended length is truncated")
	}
	length, err := readSizedUint(p.data[cursor+1:], size)
	return length, cursor + 1 + uint64(size), err
}

func readSizedUint(data []byte, size int) (uint64, error) {
	if size < 1 || size > 8 || len(data) < size {
		return 0, fmt.Errorf("invalid %d-byte integer", size)
	}
	var value uint64
	for _, item := range data[:size] {
		value = value<<8 | uint64(item)
	}
	return value, nil
}
