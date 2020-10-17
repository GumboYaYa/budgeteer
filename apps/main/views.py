from django.shortcuts import render
from .models import Transaction


def list_view(request):
    context = {}
    context["dataset"] = Transaction.objects().order_by("-date_booking")

    return render(request, "main/index.html", context)
