<script lang="ts">
  import { onDestroy } from 'svelte';
  import { t } from '$lib/i18n';
  import type { Detection } from '$lib/types/detection.types';
  import { buildAppUrl } from '$lib/utils/urlHelpers';
  import { createSpectrogramLoader } from '$lib/utils/spectrogramLoader.svelte';

  interface Props {
    detection: Detection;
    active?: boolean;
    className?: string;
    imageClassName?: string;
    fallbackClassName?: string;
  }

  let {
    detection,
    active = true,
    className = '',
    imageClassName = '',
    fallbackClassName = '',
  }: Props = $props();

  const spectrogramLoader = createSpectrogramLoader({ size: 'md', raw: true });

  let posterFailed = $state(false);
  let previousDetectionId: number | undefined;

  let showVideoPoster = $derived(
    Boolean(detection.hasVideo && !posterFailed && (detection.videoPreviewImageUrl || detection.id))
  );
  let videoPosterUrl = $derived.by(() => {
    if (!showVideoPoster) {
      return '';
    }

    if (detection.videoPreviewImageUrl) {
      return buildAppUrl(detection.videoPreviewImageUrl);
    }

    return buildAppUrl(`/api/v2/video/${detection.id}/poster`);
  });

  $effect(() => {
    if (previousDetectionId !== detection.id) {
      posterFailed = false;
      previousDetectionId = detection.id;
    }
  });

  $effect(() => {
    if (showVideoPoster) {
      spectrogramLoader.stop();
      return;
    }

    if (active) {
      spectrogramLoader.start(detection.id);
    } else {
      spectrogramLoader.stop();
    }
  });

  onDestroy(() => {
    spectrogramLoader.destroy();
  });
</script>

<div class={className}>
  {#if showVideoPoster}
    <img
      src={videoPosterUrl}
      alt={t('detections.media.videoPreviewAlt', { species: detection.commonName })}
      class={imageClassName}
      decoding="async"
      onerror={() => {
        posterFailed = true;
      }}
    />
  {:else if spectrogramLoader.showSpinner}
    <div class={fallbackClassName}>
      <span class="loading loading-spinner loading-md text-[var(--color-base-content)]/50"></span>
      {#if spectrogramLoader.isQueued}
        <span class="mt-1 text-xs text-[var(--color-base-content)]/40">
          {t('components.audio.generating')}
        </span>
      {:else if spectrogramLoader.isGenerating}
        <span class="mt-1 text-xs text-[var(--color-base-content)]/40">
          {t('components.audio.spectrogramGeneratingAria')}
        </span>
      {/if}
    </div>
  {:else if spectrogramLoader.error}
    <div class={fallbackClassName}>
      <span class="text-sm text-[var(--color-base-content)]/50">
        {t('components.audio.spectrogramUnavailable')}
      </span>
    </div>
  {:else if spectrogramLoader.spectrogramUrl}
    <img
      src={spectrogramLoader.spectrogramUrl}
      alt={t('detections.media.spectrogramPreviewAlt', { species: detection.commonName })}
      class={imageClassName}
      class:opacity-0={spectrogramLoader.state === 'loading'}
      decoding="async"
      onload={() => spectrogramLoader.handleImageLoad()}
      onerror={() => spectrogramLoader.handleImageError()}
    />
  {/if}
</div>
