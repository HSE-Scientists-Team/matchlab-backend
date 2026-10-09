package domain

import "errors"

var (
	ErrNotFound           = errors.New("файл не найден")
	ErrAlreadyExists      = errors.New("файл или расположение объекта уже существует")
	ErrInvalidStatus      = errors.New("состояние файла не допускает операцию")
	ErrInvalidArgument    = errors.New("некорректные параметры файла")
	ErrObjectNotFound     = errors.New("объект ещё не загружен")
	ErrObjectMismatch     = errors.New("метаданные загруженного объекта не соответствуют ограничениям")
	ErrStorageUnavailable = errors.New("объектное хранилище недоступно")
)
