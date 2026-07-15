import { slides } from "../slides";
import { SlideLayout } from "../components/SlideLayout";

/** Slide 07: Competition. Thin wrapper — selects its data, renders the layout. */
const data = slides.find((s) => s.id === "competition")!;
export default function Slide07Competition() {
  return <SlideLayout data={data} />;
}
