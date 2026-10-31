export interface Metric { value: number | null; unit: string; collected_at: string; last_success_at: string | null; status: string; reason: string }
export interface Fan { index: number; reported: Metric; desired_duty: number | null; commanded_duty: number | null; state: string; error: string }
export interface GPU { uuid: string; name: string; index: number; controllable: boolean; control_reason: string; min_duty: number; max_duty: number; metrics: Record<string, Metric>; fans: Fan[]; state: string; reason: string; desired_duty: number | null; applied_revision: number; updated_at: string; last_success_at: string }
export interface Status { mock: boolean; available: boolean; error: string; desired_revision: number; applied_revision: number; updated_at: string; version: string }
export interface Point { temperature: number; duty: number }
export interface Range { min_temperature: number; max_temperature: number; fan_speed: number; hysteresis: number }
export interface Curve { type: 'step' | 'linear'; points?: Point[]; ranges?: Range[] }
export interface Policy { mode: 'automatic' | 'curve'; curve: string; fans: number[]; min_duty: number; critical_temp: number; hysteresis: number; cooling_hold_seconds: number; limits_confirmed: boolean }
export interface Config { schema_version: number; revision: number; sampling_seconds: number; freshness_seconds: number; failure_limit: number; curves: Record<string, Curve>; gpus: Record<string, Policy> }
export interface History { at: string; temperature_c: number | null; desired_duty: number | null }
export interface AutomaticResult { uuid: string; restored: boolean; persisted: boolean; hardware_error: string; storage_error: string; revision: number }

export interface IPMIIdentity { board: string; manufacturer: string; firmware: string }
export interface IPMIFan { name: string; rpm: number | null; status: string; reason?: string }
export interface IPMIReading { identity: IPMIIdentity; fans: IPMIFan[]; duties: number[]; collected_at: string }
export interface IPMISnapshot { enabled: boolean; mock: boolean; state: string; reason?: string; reading: IPMIReading; owned_fans: number[]; updated_at: string; hdd?: HDDStatus }
export interface IPMISettings { schema_version: number; revision: number; profile: string; expected_identity: IPMIIdentity; min_duty: number; automatic_verified: boolean; mapping_verified: boolean; thermal_verified: boolean; fan_sensors: Record<string, string>; hdd?: HDDPolicy }
export interface IPMIResult { applied?: boolean; restored?: boolean; persisted?: boolean; hardware_error?: string; storage_error?: string; error?: string }

export interface DiskDrive { id: string; path: string; type: 'ata' | 'sat'; serial: string }
export interface DiskSample { id: string; path: string; serial: string; temperature_c: number | null; status: string; reason?: string; attempted_at: string; read_completed_at: string; last_valid_at: string }
export interface DiskSnapshot { enabled: boolean; mock: boolean; samples: DiskSample[]; updated_at: string }
export interface HDDPolicy { mode: 'automatic' | 'curve'; fan: number; drives: DiskDrive[]; curve: Curve; sampling_seconds: number; freshness_seconds: number; missing_grace_seconds: number; critical_temp: number; hysteresis: number; cooling_hold_seconds: number; shared_min_duty: number; limits_confirmed: boolean; standby_verified: boolean; shared_cooling_verified: boolean }
export interface HDDStatus { state: string; reason: string; mean_c: number | null; max_c: number | null; valid: number; expected: number; asleep: number; target: number | null; armed: boolean; samples: DiskSample[]; updated_at: string }
