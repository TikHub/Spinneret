package server

import (
	"errors"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// jsonCodec is the Connect JSON codec used by every handler. It follows the
// API conventions of the implementation spec: snake_case (proto) field names,
// zero values always emitted, unknown fields ignored on input.
type jsonCodec struct {
	marshal   protojson.MarshalOptions
	unmarshal protojson.UnmarshalOptions
}

func newJSONCodec() *jsonCodec {
	return &jsonCodec{
		marshal:   protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true},
		unmarshal: protojson.UnmarshalOptions{DiscardUnknown: true},
	}
}

// Name implements connect.Codec. Registering a codec named "json" replaces
// Connect's default JSON codec.
func (c *jsonCodec) Name() string { return "json" }

// Marshal implements connect.Codec.
func (c *jsonCodec) Marshal(v any) ([]byte, error) {
	msg, ok := v.(proto.Message)
	if !ok {
		return nil, fmt.Errorf("json codec: %T is not a proto.Message", v)
	}
	return c.marshal.Marshal(msg)
}

// MarshalAppend implements Connect's optional appender interface to reduce allocations.
func (c *jsonCodec) MarshalAppend(dst []byte, v any) ([]byte, error) {
	msg, ok := v.(proto.Message)
	if !ok {
		return nil, fmt.Errorf("json codec: %T is not a proto.Message", v)
	}
	return c.marshal.MarshalAppend(dst, msg)
}

// Unmarshal implements connect.Codec.
func (c *jsonCodec) Unmarshal(data []byte, v any) error {
	msg, ok := v.(proto.Message)
	if !ok {
		return fmt.Errorf("json codec: %T is not a proto.Message", v)
	}
	if len(data) == 0 {
		return errors.New("json codec: empty request body")
	}
	return c.unmarshal.Unmarshal(data, msg)
}
