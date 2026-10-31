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
  const width = 680, height = 270, left = 48, right = 20, top = 24, bottom = 38;
  $: minT = Math.min(minimumTemperature, ...points.map(p => p.temperature), currentTemperature ?? minimumTemperature);
  $: maxT = Math.max(maximumTemperature, ...points.map(p => p.temperature), currentTemperature ?? maximumTemperature);
  $: x = (t: number) => left + (t - minT) / (maxT - minT) * (width - left - right);
  const y = (d: number) => height - bottom - d / 100 * (height - top - bottom);
  $: path = points.length ? points.map((p, i) => i === 0 ? `M ${left} ${y(p.duty)} H ${x(p.temperature)}` : kind === 'step' ? `H ${x(p.temperature)} V ${y(p.duty)}` : `L ${x(p.temperature)} ${y(p.duty)}`).join(' ') + ` H ${width-right}` : '';
  function move(event: PointerEvent) {
    if (dragging === null || readonly) return;
    const rect = svg.getBoundingClientRect();
    const px = (event.clientX - rect.left) / rect.width * width;
    const py = (event.clientY - rect.top) / rect.height * height;
    const t = Math.round(minT + (px - left) / (width - left - right) * (maxT - minT));
    const duty = Math.round((height - bottom - py) / (height - top - bottom) * 100);
    const lo = dragging === 0 ? 0 : points[dragging - 1].temperature + 1;
    const hi = dragging === points.length - 1 ? temperatureLimit : points[dragging + 1].temperature - 1;
    onchange(dragging, Math.max(lo, Math.min(hi, t)), Math.max(0, Math.min(100, duty)));
  }
  function key(event: KeyboardEvent, i: number) {
    if (readonly) return;
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
<svg bind:this={svg} viewBox="0 0 {width} {height}" class="curve-graph" aria-label="Fan curve: temperature in Celsius and fan duty in percent" role="img">
  {#each [0, 25, 50, 75, 100] as duty}
    <line x1={left} x2={width - right} y1={y(duty)} y2={y(duty)} class="grid" />
    <text x={left - 12} y={y(duty) + 4} text-anchor="end">{duty}%</text>
  {/each}
  {#each [minT, minT + (maxT-minT)/4, minT + (maxT-minT)/2, minT + (maxT-minT)*3/4, maxT] as t}
    <text x={x(t)} y={height - 12} text-anchor="middle">{Math.round(t)}°</text>
  {/each}
  {#if currentTemperature !== null}
    <line x1={x(currentTemperature)} x2={x(currentTemperature)} y1={top} y2={height-bottom} class="current" />
    {#if target !== null}
      <circle cx={x(currentTemperature)} cy={y(target)} r="7" class="operating" />
    {/if}
  {/if}
  <path d={path} class="curve" />
  {#each points as point, i}
    <circle cx={x(point.temperature)} cy={y(point.duty)} r="6" class="point" class:locked={readonly}
      tabindex={readonly ? -1 : 0} role="slider" aria-label={`Point ${i + 1}, ${point.temperature} degrees, ${point.duty} percent. Arrow keys adjust.`}
      aria-valuemin="0" aria-valuemax="100" aria-valuenow={point.duty}
      onpointerdown={(event) => { if (!readonly) { event.preventDefault(); dragging = i; } }}
      onkeydown={(event) => key(event, i)} />
  {/each}
</svg>
<style>
  .curve-graph { display:block; width:100%; overflow:visible; touch-action:none; }
  .grid { stroke:#283333; stroke-width:1; }
  text { fill:#819594; font:11px ui-monospace, monospace; }
  .curve { fill:none; stroke:#a5e6aa; stroke-width:2.5; stroke-linejoin:round; }
  .point { fill:#10221a; stroke:#a5e6aa; stroke-width:2.5; cursor:grab; }
  .point:focus { outline:none; stroke:white; stroke-width:4; }
  .point.locked { cursor:default; }
  .current { stroke:#778ca1; stroke-width:1; stroke-dasharray:4 4; }
  .operating { fill:#9dc5fc; stroke:#101818; stroke-width:2; }
</style>
