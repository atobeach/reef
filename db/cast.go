package db

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/atobeach/reef/crypt"
)

// Cast converts a column between the value the app holds and the one stored:
// the model's writes go through Set, and a query over the model reads
// through Scanner. Generated models declare theirs in Schema.
type Cast interface {
	// Set is the stored form of a value written to the column.
	Set(ctx context.Context, value any) (any, error)
	// Scanner wraps dest, the field or value the column scans into.
	Scanner(ctx context.Context, dest any) any
}

// Encrypted is the `encrypted` cast: the column holds the text sealed with
// EncryptString by the encrypter the context carries (crypt.With), and the
// model holds it in the clear.
var Encrypted Cast = encryptedCast{}

// JSON is the `json` cast: a written value is stored as encode spells it (a
// Go string as a JSON string), a json.RawMessage is stored as given, and a
// JSON null is SQL NULL. The model field is a json.RawMessage, or the type a
// `json:<Type>` cast names, which pgx decodes the column into.
func JSON(encode func(any) ([]byte, error)) Cast {
	return jsonCast{encode: encode}
}

// EncryptedJSON is the `encrypted:json` cast: the value as JSON casts spell
// it, sealed as Encrypted seals text. The model field is a json.RawMessage
// holding the opened JSON.
func EncryptedJSON(encode func(any) ([]byte, error)) Cast {
	return encryptedJSONCast{json: jsonCast{encode: encode}}
}

type jsonCast struct {
	encode func(any) ([]byte, error)
}

func (c jsonCast) Set(_ context.Context, value any) (any, error) {
	switch v := value.(type) {
	case nil:
		return nil, nil
	case json.RawMessage:
		if v == nil {
			return nil, nil
		}

		return string(v), nil
	}

	encoded, err := c.encode(value)
	if err != nil {
		return nil, err
	}

	if string(encoded) == "null" {
		return nil, nil
	}

	return string(encoded), nil
}

// Scanner leaves dest as is: pgx reads a json column into json.RawMessage
// as stored, and decodes it into any other type with encoding/json (a typed
// field's UnmarshalJSON), failing the read on its error.
func (jsonCast) Scanner(_ context.Context, dest any) any {
	return dest
}

type encryptedCast struct{}

func (encryptedCast) Set(ctx context.Context, value any) (any, error) {
	switch v := value.(type) {
	case nil:
		return nil, nil
	case *string:
		if v == nil {
			return nil, nil
		}

		return seal(ctx, *v)
	case string:
		return seal(ctx, v)
	}

	// Never written in the clear: a value that isn't text fails the write.
	return nil, fmt.Errorf("an encrypted column takes text, not %T", value)
}

func (encryptedCast) Scanner(ctx context.Context, dest any) any {
	return opener{ctx: ctx, dest: dest}
}

type encryptedJSONCast struct {
	json jsonCast
}

func (c encryptedJSONCast) Set(ctx context.Context, value any) (any, error) {
	encoded, err := c.json.Set(ctx, value)
	if err != nil || encoded == nil {
		return nil, err
	}

	return seal(ctx, encoded.(string))
}

func (encryptedJSONCast) Scanner(ctx context.Context, dest any) any {
	return opener{ctx: ctx, dest: dest}
}

func seal(ctx context.Context, plain string) (any, error) {
	encrypter, err := crypt.FromContext(ctx)
	if err != nil {
		return nil, err
	}

	return encrypter.EncryptString(plain)
}

// opener scans a sealed column into its destination, opened: a string, a
// *string (nil for NULL), json.RawMessage, or an any holding the text (a map
// row's value).
type opener struct {
	ctx  context.Context
	dest any
}

func (o opener) Scan(src any) error {
	var sealed string
	switch v := src.(type) {
	case nil:
		return o.assign(nil)
	case string:
		sealed = v
	case []byte:
		sealed = string(v)
	default:
		return fmt.Errorf("db: an encrypted column holds text, not %T", src)
	}

	encrypter, err := crypt.FromContext(o.ctx)
	if err != nil {
		return err
	}

	plain, err := encrypter.DecryptString(sealed)
	if err != nil {
		return err
	}

	return o.assign(&plain)
}

func (o opener) assign(plain *string) error {
	switch dest := o.dest.(type) {
	case *string:
		*dest = ""
		if plain != nil {
			*dest = *plain
		}
	case **string:
		*dest = plain
	case *json.RawMessage:
		*dest = nil
		if plain != nil {
			*dest = json.RawMessage(*plain)
		}
	case *any:
		*dest = nil
		if plain != nil {
			*dest = *plain
		}
	default:
		return fmt.Errorf("db: an encrypted column scans into a string, *string, json.RawMessage or any, not %T", o.dest)
	}

	return nil
}
