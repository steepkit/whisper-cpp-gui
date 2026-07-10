package model

type modelLock interface {
	close() error
}
