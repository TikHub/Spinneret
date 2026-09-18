package spinneret

import (
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// jsonCodec is the Connect JSON codec of the SDK. It sends snake_case (proto)
// field names like the documented wire format and ignores unknown response
// fields, so that an older SDK keeps working against a newer server.
type jsonCodec struct {
	marshal   protojson.MarshalOptions
	unmarshal protojson.UnmarshalOptions
}

func newJSONCodec() *jsonCodec {
	return &jsonCodec{
		marshal:   protojson.MarshalOptions{UseProtoNames: true},
		unmarshal: protojson.UnmarshalOptions{DiscardUnknown: true},
	}
}

// Name implements connect.Codec; "json" selects the application/json content type.
func (c *jsonCodec) Name() string { return "json" }

// Marshal implements connect.Codec.
func (c *jsonCodec) Marshal(v any) ([]byte, error) {
	msg, ok := v.(proto.Message)
	if !ok {
		return nil, fmt.Errorf("spinneret json codec: %T is not a proto.Message", v)
	}
	return c.marshal.Marshal(msg)
}

// Unmarshal implements connect.Codec. An empty body decodes to the zero message.
func (c *jsonCodec) Unmarshal(data []byte, v any) error {
	msg, ok := v.(proto.Message)
	if !ok {
		return fmt.Errorf("spinneret json codec: %T is not a proto.Message", v)
	}
	if len(data) == 0 {
		proto.Reset(msg)
		return nil
	}
	return c.unmarshal.Unmarshal(data, msg)
}
