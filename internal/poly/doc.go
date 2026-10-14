// Package poly implements univariate polynomial arithmetic over F = GF(2^255−19) for the
// receiver's interpolation and the sender's multipoint evaluation.
//
// Multiplication dispatches between schoolbook, Karatsuba and the RNS-NTT of package rns.
// Large point sets use a subproduct tree with leaf blocks of LeafSize points; evaluation
// walks a scaled remainder tree whose steps are middle products (one transform of the
// node's degree per child); interpolation evaluates M' with the same tree and
// combines Lagrange weights bottom-up. Sets of at most LeafSize points are a single leaf
// and take the quadratic path (Horner / synthetic division), which is what batched mode's
// small instances use.
package poly
