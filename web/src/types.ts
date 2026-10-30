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
