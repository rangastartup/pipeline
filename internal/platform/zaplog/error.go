// Copyright © 2018 Banzai Cloud
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package zaplog

import (
	"github.com/goph/emperror"
	"go.uber.org/zap"
)

// LogError logs an error.
func LogError(logger *zap.Logger, err error) {
	errCtx := emperror.Context(err)
	if len(errCtx) > 0 {
		for i := 0; i < len(errCtx); i += 2 {
			key := errCtx[i].(string)

			logger = logger.With(zap.Any(key, errCtx[i+1]))
		}
	}

	logger.Error(err.Error())
}
