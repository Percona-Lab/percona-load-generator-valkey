package valkey

import (
	"context"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/values"
)

// preloadCmd returns the one command that creates key i of the five basic types.
func preloadCmd(d config.Dataset, typ string, i int64, src *values.Source, r *rand.Rand) Cmd {
	switch typ {
	case config.TypeString:
		k := KeyName(d, nsString, i)
		if d.TTL.Mode != "none" {
			secs := d.TTL.Seconds
			if d.TTL.Mode == "random" {
				secs = d.TTL.MinSeconds + r.Intn(d.TTL.MaxSeconds-d.TTL.MinSeconds+1)
			}
			if d.TTL.Fraction >= 1 || r.Float64() < d.TTL.Fraction {
				return Cmd{"SET", k, src.Next(r), "EX", strconv.Itoa(secs)}
			}
		}
		return Cmd{"SET", k, src.Next(r)}
	case config.TypeHash:
		c := Cmd{"HSET", KeyName(d, nsHash, i)}
		for f := 0; f < d.HashFields; f++ {
			c = append(c, "f"+strconv.Itoa(f), src.Next(r))
		}
		return c
	case config.TypeList:
		c := Cmd{"RPUSH", KeyName(d, nsList, i)}
		for j := 0; j < d.CollectionSize; j++ {
			c = append(c, src.Next(r))
		}
		return c
	case config.TypeSet:
		c := Cmd{"SADD", KeyName(d, nsSet, i)}
		for j := 0; j < d.CollectionSize; j++ {
			c = append(c, "m"+strconv.Itoa(j))
		}
		return c
	default: // zset
		c := Cmd{"ZADD", KeyName(d, nsZSet, i)}
		for j := 0; j < d.CollectionSize; j++ {
			c = append(c, strconv.Itoa(r.Intn(1_000_000)), "m"+strconv.Itoa(j))
		}
		return c
	}
}

// preloadCmds returns the commands that create key i of a type. Most types are
// one command; a stream is a consumer group plus its entries.
func preloadCmds(d config.Dataset, typ string, i int64, src *values.Source, r *rand.Rand) []Cmd {
	switch typ {
	case config.TypeStream:
		key := KeyName(d, nsStream, i)
		cmds := []Cmd{{"XGROUP", "CREATE", key, StreamGroup, "0", "MKSTREAM"}}
		for n := 0; n < d.StreamLength; n++ {
			c := Cmd{"XADD", key, "*"}
			for f := 0; f < 2; f++ {
				c = append(c, "f"+strconv.Itoa(f), src.Next(r))
			}
			cmds = append(cmds, c)
		}
		return cmds
	case config.TypeGeo:
		c := Cmd{"GEOADD", KeyName(d, nsGeo, i)}
		for j := 0; j < d.CollectionSize; j++ {
			c = append(c, strconv.FormatFloat(-10+r.Float64()*20, 'f', 5, 64), strconv.FormatFloat(40+r.Float64()*10, 'f', 5, 64), "m"+strconv.Itoa(j))
		}
		return []Cmd{c}
	case config.TypeHLL:
		c := Cmd{"PFADD", KeyName(d, nsHLL, i)}
		for j := 0; j < d.CollectionSize; j++ {
			c = append(c, "e"+strconv.Itoa(r.Intn(1_000_000)))
		}
		return []Cmd{c}
	case config.TypeBitmap:
		b := make([]byte, d.BitmapBits/8)
		for j := range b {
			b[j] = byte(r.Intn(256))
		}
		return []Cmd{{"SET", KeyName(d, nsBitmap, i), string(b)}}
	}
	return []Cmd{preloadCmd(d, typ, i, src, r)}
}

// Preload creates every key of each type the mix uses, so reads find data.
func Preload(ctx context.Context, c Client, cfg config.Config) error {
	src, err := values.NewSource(cfg.Dataset.ValueSize, cfg.Run.Seed)
	if err != nil {
		return err
	}
	for _, typ := range config.AllTypes {
		if !config.UsedTypes(cfg.Workload.Mix)[typ] {
			continue
		}
		if err := preloadType(ctx, c, cfg, typ, src); err != nil {
			return fmt.Errorf("preload %s: %w", typ, err)
		}
	}
	return nil
}

func preloadType(ctx context.Context, c Client, cfg config.Config, typ string, src *values.Source) error {
	const batch = 100
	var next atomic.Int64
	var wg sync.WaitGroup
	errc := make(chan error, cfg.Run.Concurrency)
	for w := 0; w < cfg.Run.Concurrency; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(cfg.Run.Seed + int64(w)))
			for {
				start := next.Add(batch) - batch
				if start >= cfg.Dataset.KeyCount || ctx.Err() != nil {
					return
				}
				end := min(start+batch, cfg.Dataset.KeyCount)
				cmds := make([]Cmd, 0, int(end-start))
				for i := start; i < end; i++ {
					cmds = append(cmds, preloadCmds(cfg.Dataset, typ, i, src, r)...)
				}
				for _, res := range c.Exec(ctx, cmds) {
					// Preloading twice finds the stream's group already there.
					if res.Err != nil && !strings.HasPrefix(res.Err.Error(), "BUSYGROUP") {
						errc <- res.Err
						return
					}
				}
			}
		}(w)
	}
	wg.Wait()
	close(errc)
	return <-errc
}
