import { redirect } from "@sveltejs/kit";

export function load({ params }: { params: { slug: string } }): never {
  redirect(307, `/library?topic_id=${encodeURIComponent(params.slug)}`);
}
