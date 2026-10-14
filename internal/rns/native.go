package rns

import "sync"

// nativeBackend uses the in-house cyclic NTT of package ntt.
type nativeBackend struct{}

// Native returns the in-house NTT backend (default).
func Native() Backend { return nativeBackend{} }

func (nativeBackend) Name() string { return "native" }

func (nativeBackend) Transform(n int) (Transform, error) {
	if err := checkTransformLen(n); err != nil {
		return nil, err
	}
	return nativeTransform{n: n}, nil
}

type nativeTransform struct{ n int }

func (t nativeTransform) Len() int { return t.n }

func (t nativeTransform) Forward(r Residues) {
	perPrime(func(j int) { Primes[j].Forward(r[j][:t.n]) })
}

func (t nativeTransform) Inverse(r Residues) {
	perPrime(func(j int) { Primes[j].Inverse(r[j][:t.n]) })
}

func (t nativeTransform) MulPointwise(dst, a, b Residues) {
	perPrime(func(j int) { Primes[j].MulPointwise(dst[j][:t.n], a[j][:t.n], b[j][:t.n]) })
}

// perPrime runs fn(j) for every prime concurrently.
func perPrime(fn func(j int)) {
	var wg sync.WaitGroup
	wg.Add(K - 1)
	for j := 1; j < K; j++ {
		go func() {
			defer wg.Done()
			fn(j)
		}()
	}
	fn(0)
	wg.Wait()
}
