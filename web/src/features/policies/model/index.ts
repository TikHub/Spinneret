import { type PolicyKind } from '../constants';
import { parseYaml, YamlSubsetError } from '../yaml/parse';
import { actionToYaml, readActionModel, type ActionModel } from './action';
import { breakerToYaml, readBreakerModel, type BreakerModel } from './breaker';
import { asObject, ModelReadError, type YamlObject } from './common';
import { readRotationModel, rotationToYaml, type RotationModel } from './rotation';
import { readSignalModel, signalToYaml, type SignalModel } from './signal';

export interface PolicyModels {
  rotation: RotationModel;
  signal: SignalModel;
  action: ActionModel;
  breaker: BreakerModel;
}

interface Codec<K extends PolicyKind> {
  read: (doc: YamlObject) => PolicyModels[K];
  write: (model: PolicyModels[K]) => string;
}

const CODECS: { [K in PolicyKind]: Codec<K> } = {
  rotation: { read: readRotationModel, write: rotationToYaml },
  signal: { read: readSignalModel, write: signalToYaml },
  action: { read: readActionModel, write: actionToYaml },
  breaker: { read: readBreakerModel, write: breakerToYaml },
};

export type ModelLoadResult<K extends PolicyKind> =
  { ok: true; model: PolicyModels[K] } | { ok: false; error: string; line?: number };

/**
 * Loads policy YAML into the form model of its kind. Fails (without throwing)
 * when the YAML uses syntax outside the supported subset or fields the form
 * cannot represent; the YAML editor stays available in that case.
 */
export function loadPolicyModel<K extends PolicyKind>(kind: K, yaml: string): ModelLoadResult<K> {
  const codec: Codec<K> = CODECS[kind];
  try {
    return { ok: true, model: codec.read(asObject(parseYaml(yaml), '')) };
  } catch (err) {
    if (err instanceof YamlSubsetError) return { ok: false, error: err.message, line: err.line };
    if (err instanceof ModelReadError) return { ok: false, error: err.message };
    throw err;
  }
}

/** Generates YAML from a form model. */
export function policyModelToYaml<K extends PolicyKind>(kind: K, model: PolicyModels[K]): string {
  const codec: Codec<K> = CODECS[kind];
  return codec.write(model);
}
