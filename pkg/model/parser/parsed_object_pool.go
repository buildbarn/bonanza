package parser

import (
	"context"
	"sync"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/eviction"
)

type ParsedObjectEvictionKey struct {
	// TODO: Have a stable key for identifying readers. That will
	// allow us to get cache hits between builds.
	reader    any
	reference model_core.Decodable[object.LocalReference]
}

type cachedParsedObject struct {
	parsedObject any
}

type ParsedObjectPool struct {
	lock               sync.Mutex
	objects            map[ParsedObjectEvictionKey]cachedParsedObject
	evictionSet        eviction.Set[ParsedObjectEvictionKey]
	remainingCount     int
	remainingSizeBytes int
}

func NewParsedObjectPool(evictionSet eviction.Set[ParsedObjectEvictionKey], maximumCount, maximumSizeBytes int) *ParsedObjectPool {
	return &ParsedObjectPool{
		objects:            map[ParsedObjectEvictionKey]cachedParsedObject{},
		evictionSet:        evictionSet,
		remainingCount:     maximumCount,
		remainingSizeBytes: maximumSizeBytes,
	}
}

type ParsedObjectPoolIngester[TReference any] struct {
	pool      *ParsedObjectPool
	rawReader ObjectReader[TReference, model_core.Message[[]byte, TReference]]
}

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
	parsedObjectReader          ObjectReader[model_core.Decodable[TReference], TParsedObject]
	decodingParametersSizeBytes int
}

func LookupParsedObjectReader[TReference object.BasicReference, TParsedObject any](
	ingester *ParsedObjectPoolIngester[TReference],
	parser ObjectParser[TReference, TParsedObject],
) ObjectReader[model_core.Decodable[TReference], TParsedObject] {
	return &poolBackedObjectReader[TReference, TParsedObject]{
		pool:                        ingester.pool,
		parsedObjectReader:          NewParsedObjectReader(ingester.rawReader, parser),
		decodingParametersSizeBytes: parser.GetDecodingParametersSizeBytes(),
	}
}

func (r *poolBackedObjectReader[TReference, TParsedObject]) ReadParsedObject(ctx context.Context, reference model_core.Decodable[TReference]) (TParsedObject, error) {
	insertionKey := ParsedObjectEvictionKey{
		reference: model_core.CopyDecodable(reference, reference.Value.GetLocalReference()),
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

	parsedObject, err := r.parsedObjectReader.ReadParsedObject(ctx, reference)
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
