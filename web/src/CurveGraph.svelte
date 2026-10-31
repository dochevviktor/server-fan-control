<script lang="ts">
  export let points: { temperature: number; duty: number }[] = [];
  export let kind = 'linear';
  export let currentTemperature: number | null = null;
  export let target: number | null = null;
  export let readonly = false;
  export let minimumTemperature = 20;
  export let maximumTemperature = 100;
  export let temperatureLimit = 200;
  export let onchange: (index: number, temperature: number, duty: number) => void = () => {};
  let svg: SVGSVGElement;
  let dragging: number | null = null;
  let measuredWidth = 680;
  $: width = Math.max(320, Math.min(680, measuredWidth));
  const height = 270, left = 48, right = 20, top = 24, bottom = 38;
  $: validPoints = points.every(p => Number.isFinite(p.temperature) && Number.isFinite(p.duty));
  $: plottedPoints = validPoints ? points : [];
  $: current = Number.isFinite(currentTemperature) ? currentTemperature : null;
  $: minT = Math.min(minimumTemperature, ...plottedPoints.map(p => p.temperature), current ?? minimumTemperature);
  $: maxT = Math.max(minT + 1, maximumTemperature, ...plottedPoints.map(p => p.temperature), current ?? maximumTemperature);
  $: x = (t: number) => left + (t - minT) / (maxT - minT) * (width - left - right);
  const y = (d: number) => height - bottom - d / 100 * (height - top - bottom);
  $: path = plottedPoints.length ? plottedPoints.map((p, i) => i === 0 ? `M ${left} ${y(p.duty)} H ${x(p.temperature)}` : kind === 'step' ? `H ${x(p.temperature)} V ${y(p.duty)}` : `L ${x(p.temperature)} ${y(p.duty)}`).join(' ') + ` H ${width-right}` : '';
  function move(event: PointerEvent) {
    if (dragging === null || readonly || !validPoints) return;
    const rect = svg.getBoundingClientRect();
    if (!rect.width || !rect.height) { dragging = null; return; }
    const px = (event.clientX - rect.left) / rect.width * width;
    const py = (event.clientY - rect.top) / rect.height * height;
    const t = Math.round(minT + (px - left) / (width - left - right) * (maxT - minT));
    const duty = Math.round((height - bottom - py) / (height - top - bottom) * 100);
    const lo = dragging === 0 ? 0 : points[dragging - 1].temperature + 1;
    const hi = dragging === points.length - 1 ? temperatureLimit : points[dragging + 1].temperature - 1;
    onchange(dragging, Math.max(lo, Math.min(hi, t)), Math.max(0, Math.min(100, duty)));
  }
  function key(event: KeyboardEvent, i: number) {
    if (readonly || !validPoints) return;
    const p = points[i];
    let t = p.temperature, d = p.duty;
    if (event.key === 'ArrowUp') d++;
    else if (event.key === 'ArrowDown') d--;
    else if (event.key === 'ArrowRight') t++;
    else if (event.key === 'ArrowLeft') t--;
    else return;
    event.preventDefault();
    if (t < 0 || t > temperatureLimit || d < 0 || d > 100) return;
    if (i > 0 && t <= points[i - 1].temperature || i + 1 < points.length && t >= points[i + 1].temperature) return;
    onchange(i, t, d);
  }
</script>

<svelte:window onpointermove={move} onpointerup={() => dragging = null} onpointercancel={() => dragging = null} />
<div class="graph-container" bind:clientWidth={measuredWidth}>
<svg bind:this={svg} viewBox="0 0 {width} {height}" class="curve-graph" aria-label="Fan curve: temperature in Celsius and fan duty in percent" role="group">
  {#each [0, 25, 50, 75, 100] as duty}
    <line x1={left} x2={width - right} y1={y(duty)} y2={y(duty)} class="grid" />
    <text x={left - 12} y={y(duty) + 4} text-anchor="end">{duty}%</text>
  {/each}
  {#each [minT, minT + (maxT-minT)/4, minT + (maxT-minT)/2, minT + (maxT-minT)*3/4, maxT] as t}
    <text x={x(t)} y={height - 12} text-anchor="middle">{Math.round(t)}°</text>
  {/each}
  {#if current !== null}
    <line x1={x(current)} x2={x(current)} y1={top} y2={height-bottom} class="current" />
    {#if target !== null && Number.isFinite(target)}
      <circle cx={x(current)} cy={y(target)} r="7" class="operating" />
    {/if}
  {/if}
  <path d={path} class="curve" />
  {#each plottedPoints as point, i}
    <circle cx={x(point.temperature)} cy={y(point.duty)} r="6" class="point" aria-hidden="true" />
    <circle cx={x(point.temperature)} cy={y(point.duty)} r="22" class="point-target" class:locked={readonly}
      tabindex={readonly ? -1 : 0} role="slider" aria-label={`Point ${i + 1}, ${point.temperature} degrees, ${point.duty} percent. Arrow keys adjust.`}
      aria-valuemin="0" aria-valuemax="100" aria-valuenow={point.duty} aria-valuetext={`${point.temperature}°C, ${point.duty}% fan speed`} aria-disabled={readonly}
      onpointerdown={(event) => { if (!readonly) { event.preventDefault(); event.currentTarget.focus(); event.currentTarget.setPointerCapture(event.pointerId); dragging = i; } }}
      onkeydown={(event) => key(event, i)} />
  {/each}
</svg>
</div>
<style>
  .graph-container { width:100%; min-width:0; }
  .curve-graph { display:block; width:100%; overflow:visible; touch-action:pan-y; }
  .grid { stroke:#283333; stroke-width:1; }
  text { fill:var(--muted, #a5b5b0); font:12px ui-monospace, monospace; }
  .curve { fill:none; stroke:#a5e6aa; stroke-width:2.5; stroke-linejoin:round; }
  .point { fill:#10221a; stroke:#a5e6aa; stroke-width:2.5; pointer-events:none; }
  .point-target { fill:transparent; stroke:transparent; cursor:grab; touch-action:none; }
  .point-target:focus-visible { outline:none; stroke:var(--accent, #a5e6aa); stroke-width:2; }
  .point-target:active { cursor:grabbing; }
  .point-target.locked { cursor:default; touch-action:pan-y; }
  .current { stroke:#778ca1; stroke-width:1; stroke-dasharray:4 4; }
  .operating { fill:#9dc5fc; stroke:#101818; stroke-width:2; }
</style>
