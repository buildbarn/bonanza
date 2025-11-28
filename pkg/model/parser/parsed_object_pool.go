package parser

import (
	"context"
	"sync"
	"unique"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/eviction"
)

// objectParserKeyListEntry is a linked list of object parser keys. This
// is used to uniquely types of object parsers, even if multiple
// instances of the same kind exist.
//
// We need to use a linked list here, as there is no way to
// unique.Make() slices. Object parsers can have zero or more keys,
// depending on how many decoding/parsing steps are performed.
//
// More details: https://github.com/golang/go/issues/73598
type objectParserKeyListEntry struct {
	parent unique.Handle[objectParserKeyListEntry]
	key    unique.Handle[any]
}

// ParsedObjectEvictionKey is the key that ParsedObjectPool uses to
// identify cached objects. It needs to be used as the value type of the
// eviction set that is used to remove cached objects if the maximum
// size of the pool has been reached.
type ParsedObjectEvictionKey struct {
	parserKeyList unique.Handle[objectParserKeyListEntry]
	reference     model_core.Decodable[object.LocalReference]
}

type cachedParsedObject struct {
	parsedObject any
}

// ParsedObjectPool is a pool of recently accessed objects, for which
// the parsed contents are stored. This allows repeated access to the
// same object to skip downloading and parsing.
type ParsedObjectPool struct {
	lock               sync.Mutex
	objects            map[ParsedObjectEvictionKey]cachedParsedObject
	evictionSet        eviction.Set[ParsedObjectEvictionKey]
	remainingCount     int
	remainingSizeBytes int
}

// NewParsedObjectPool creates a ParsedObjectPool that is initially
// empty.
func NewParsedObjectPool(evictionSet eviction.Set[ParsedObjectEvictionKey], maximumCount, maximumSizeBytes int) *ParsedObjectPool {
	return &ParsedObjectPool{
		objects:            map[ParsedObjectEvictionKey]cachedParsedObject{},
		evictionSet:        evictionSet,
		remainingCount:     maximumCount,
		remainingSizeBytes: maximumSizeBytes,
	}
}

// ParsedObjectPoolIngester associates a ParsedObjectPool with an
// ObjectReader that is responsible for reading raw object contents from
// storage.
type ParsedObjectPoolIngester[TReference any] struct {
	pool      *ParsedObjectPool
	rawReader ObjectReader[TReference, model_core.Message[[]byte, TReference]]
}

// NewParsedObjectPoolIngester creates a ParsedObjectPoolIngester,
// thereby associating ParsedObjectPool with a ParsedObjectReader for
// reading raw object contents from storage.
//
// It is permitted to set the provided pool to nil. In that case
// LookupParsedObjectReader() will create readers that do not perform
// any caching at all. Any attempt to read a parsed object will lead to
// the object's contents to be read from storage.
func NewParsedObjectPoolIngester[TReference any](
	pool *ParsedObjectPool,
	rawReader ObjectReader[TReference, model_core.Message[[]byte, TReference]],
) *ParsedObjectPoolIngester[TReference] {
	return &ParsedObjectPoolIngester[TReference]{
		pool:      pool,
		rawReader: rawReader,
	}
}

type poolBackedObjectReader[TReference object.BasicReference, TParsedObject any] struct {
	pool                        *ParsedObjectPool
	parsedObjectReader          DecodingObjectReader[TReference, TParsedObject]
	parserKeyList               unique.Handle[objectParserKeyListEntry]
	decodingParametersSizeBytes int
}

// LookupParsedObjectReader returns an object reader that can be used to
// read parsed contents of an object (e.g., unmarshaled Protobuf
// messages), given a parser. If a pool is provided, the parsed object
// contents will be stored in the pool, so that subsequent attempts to
// access the objects return the previously parsed contents.
func LookupParsedObjectReader[TReference object.BasicReference, TParsedObject any](
	ingester *ParsedObjectPoolIngester[TReference],
	parser ObjectParser[TReference, TParsedObject],
) DecodingObjectReader[TReference, TParsedObject] {
	objectReader := NewParsedObjectReader(ingester.rawReader, parser)
	if ingester.pool != nil {
		// Perform caching of objects.
		var parserKeyList unique.Handle[objectParserKeyListEntry]
		for _, parserKey := range parser.AppendUniqueKeys(nil) {
			parserKeyList = unique.Make(
				objectParserKeyListEntry{
					parent: parserKeyList,
					key:    parserKey,
				},
			)
		}
		objectReader = &poolBackedObjectReader[TReference, TParsedObject]{
			pool:                        ingester.pool,
			parsedObjectReader:          objectReader,
			parserKeyList:               parserKeyList,
			decodingParametersSizeBytes: parser.GetDecodingParametersSizeBytes(),
		}
	}
	return objectReader
}

func (r *poolBackedObjectReader[TReference, TParsedObject]) ReadObject(ctx context.Context, reference model_core.Decodable[TReference]) (TParsedObject, error) {
	insertionKey := ParsedObjectEvictionKey{
		parserKeyList: r.parserKeyList,
		reference:     model_core.CopyDecodable(reference, reference.Value.GetLocalReference()),
	}

	p := r.pool
	p.lock.Lock()
	if object, ok := p.objects[insertionKey]; ok {
		// Return cached instance of the parsed object.
		p.evictionSet.Touch(insertionKey)
		p.lock.Unlock()
		return object.parsedObject.(TParsedObject), nil
	}
	p.lock.Unlock()

	parsedObject, err := r.parsedObjectReader.ReadObject(ctx, reference)
	if err != nil {
		var badParsedObject TParsedObject
		return badParsedObject, err
	}

	p.lock.Lock()
	if _, ok := p.objects[insertionKey]; ok {
		// Race: parsed object was inserted into the cache by
		// another goroutine while we were parsing it as well.
		p.evictionSet.Touch(insertionKey)
	} else {
		p.objects[insertionKey] = cachedParsedObject{
			parsedObject: parsedObject,
		}
		p.remainingCount--
		p.remainingSizeBytes -= reference.Value.GetSizeBytes()
		p.evictionSet.Insert(insertionKey)

		// Evict objects if we're consuming too much space.
		for p.remainingCount < 0 || p.remainingSizeBytes < 0 {
			removalKey := p.evictionSet.Peek()
			delete(p.objects, removalKey)

			p.remainingCount++
			p.remainingSizeBytes += removalKey.reference.Value.GetSizeBytes()
			p.evictionSet.Remove()
		}
	}
	p.lock.Unlock()

	return parsedObject, nil
}

func (r *poolBackedObjectReader[TReference, TParsedObject]) GetDecodingParametersSizeBytes() int {
	return r.decodingParametersSizeBytes
}
